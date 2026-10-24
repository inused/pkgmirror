// 删除接口接收客户端 JSON 和网页表单，按镜像元数据构建依赖图，
// 计算级联清单并删除文件。设计见 docs/删除设计.md。
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"pkgmirror/internal/golang"
	"pkgmirror/internal/gomod"
	"pkgmirror/internal/npm"
	"pkgmirror/internal/pypi"
)

// deleteRequest 是删除请求，JSON 字段和网页表单均映射至此结构。
type deleteRequest struct {
	Type   string `json:"type"`   // 镜像类型：pypi/python/npm/node/golang/gomod
	Name   string `json:"name"`   // 包名、模块路径、安装包版本或归档文件名
	Deps   bool   `json:"deps"`   // true 级联依赖或安装包版本组；false 只删目标
	DryRun bool   `json:"dryRun"` // true 预览清单；false 执行删除
}

// deleteFile 是删除清单中的一个文件。
type deleteFile struct {
	Path string `json:"path"` // 相对 root 的路径，/ 分隔
	Size int64  `json:"size"` // 字节数
}

// deleteUnit 是一个删除单元（整包 / 整模块 / 版本组）的展示清单。
type deleteUnit struct {
	Name  string       `json:"name"`  // 包名 / 模块路径 / 版本参数
	Files []deleteFile `json:"files"` // 将删文件（含 .sha256 伴随文件）
	Size  int64        `json:"size"`  // 文件总大小
}

// deleteResult 是 /api/delete 的 JSON 响应。
type deleteResult struct {
	Status   string       `json:"status"`             // preview（dryRun）或 ok（已删除）
	Targets  []deleteUnit `json:"targets"`            // 删除目标
	Cascades []deleteUnit `json:"cascades,omitempty"` // 级联依赖或版本组其余文件，为空时省略
	Count    int          `json:"count"`              // 文件总数（目标 + 级联）
	Total    int64        `json:"total"`              // 字节总数（目标 + 级联）
}

// deleteAction 是一个单元的展示清单与执行动作。
type deleteAction struct {
	unit   deleteUnit
	remove func() error
}

// handleDeleteAPI 处理删除（客户端 JSON 与网页表单共用）。
//
// JSON 请求字段见 deleteRequest，dryRun=true 预览清单。网页表单以
// dryRun=1 进入预览页，执行后 303 返回对应类型列表并显示结果。
// 仅接受 POST，防止链接预取或爬虫触发删除。
//
//	@param w http.ResponseWriter 响应写入器。
//	@param r *http.Request JSON 或 urlencoded 表单请求。
func (s *Server) handleDeleteAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	var req deleteRequest
	form := false
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		form = true
		if err := r.ParseForm(); err != nil {
			http.Error(w, "解析表单失败: "+err.Error(), http.StatusBadRequest)
			return
		}
		// 六类列表均用 dryRun=1 预览；执行表单以 deps=1 选择级联删除，
		// 两字段都未设置时直接删除目标。
		req = deleteRequest{
			Type:   r.PostFormValue("type"),
			Name:   r.PostFormValue("name"),
			Deps:   r.PostFormValue("deps") == "1",
			DryRun: r.PostFormValue("dryRun") == "1",
		}
	} else {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		if err != nil {
			http.Error(w, "读取请求失败: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "请求体须是 JSON {type,name,deps,dryRun}: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	// 预览页展示目标和可级联内容，由执行按钮选择删除范围。
	if form && req.DryRun {
		s.renderDeletePreview(w, req)
		return
	}
	result, status, err := s.planAndDelete(req)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if !req.DryRun {
		cascadeDesc := "无"
		if len(result.Cascades) > 0 {
			names := make([]string, len(result.Cascades))
			for i, c := range result.Cascades {
				names[i] = c.Name
			}
			cascadeDesc = strings.Join(names, ",")
		}
		log.Printf("已删除 ip=%s %s %s 文件=%d 释放=%s 级联=%s", clientIP(r), req.Type, req.Name, result.Count, humanSize(result.Total), cascadeDesc)
	}
	if form {
		// 回跳固定为该类型列表页而非 Referer：预览页地址栏停在
		// /api/delete，其表单提交的 Referer 即本接口，重定向回它会被
		// 303 转成 GET /api/delete 而 405；固定映射也免去 Referer 可控
		// 的开放重定向面
		back := previewBack[req.Type]
		if back == "" {
			back = "/"
		}
		msg := fmt.Sprintf("已删除 %s（%s）：%d 个文件，释放 %s", req.Type, req.Name, result.Count, humanSize(result.Total))
		if len(result.Cascades) > 0 {
			msg += fmt.Sprintf("，含级联 %d 项", len(result.Cascades))
		}
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// previewBack 是预览页"取消返回"的目标：各类型的列表页路径。
var previewBack = map[string]string{
	"pypi":   "/browse/pypi/",
	"npm":    "/browse/npm/",
	"gomod":  "/browse/gomod/",
	"python": "/browse/python/",
	"node":   "/browse/node/",
	"golang": "/browse/golang/",
}

// renderDeletePreview 渲染删除预览页（表单 dryRun 路径）。
//
// 预览目标与可级联清单，并计算直接删除、级联删除的文件数与大小。
// 安装包版本目标按整组预览，文件名目标可展示同版本其余文件。
// 目标不存在等错误按对应状态码返回。
//
//	@param w http.ResponseWriter 响应写入器。
//	@param req deleteRequest 表单请求（Type/Name 有效，DryRun 已置位）。
func (s *Server) renderDeletePreview(w http.ResponseWriter, req deleteRequest) {
	// 包/模块目标与安装包文件名目标展示可级联清单；安装包版本
	// 形态没有扩展（本身即整组），deps 必须为 false 才能通过校验
	deps := !isInstallerType(req.Type) || installerFileArg(req.Type, req.Name)
	result, status, err := s.planAndDelete(deleteRequest{Type: req.Type, Name: req.Name, Deps: deps, DryRun: true})
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	var targetCount, cascadeCount int
	var targetSize, cascadeSize int64
	for _, u := range result.Targets {
		targetCount += len(u.Files)
		targetSize += u.Size
	}
	for _, u := range result.Cascades {
		cascadeCount += len(u.Files)
		cascadeSize += u.Size
	}
	totalCount, totalSize := targetCount+cascadeCount, targetSize+cascadeSize
	// 按钮文案只带文件数（对象名在区块标题与表格中）；级联区块标题按
	// 类型区分语义。无扩展内容时级联区块整体不渲染（模板 {{if .Cascades}}）
	btnSelf := fmt.Sprintf("直接删除（%d 个文件）", targetCount)
	btnFull := ""
	cascadeTitle := "级联依赖（不再被其他包引用）"
	if len(result.Cascades) > 0 {
		btnFull = fmt.Sprintf("级联删除（%d 个文件）", totalCount)
		if isInstallerType(req.Type) {
			cascadeTitle = "版本组其余文件"
		}
	}
	render(w, deletePreviewTpl, map[string]any{
		"Type":         req.Type,
		"Name":         req.Name,
		"Targets":      result.Targets,
		"Cascades":     result.Cascades,
		"TargetCount":  targetCount,
		"TargetSize":   targetSize,
		"CascadeCount": cascadeCount,
		"CascadeSize":  cascadeSize,
		"TotalCount":   totalCount,
		"TotalSize":    totalSize,
		"BtnSelf":      btnSelf,
		"BtnFull":      btnFull,
		"CascadeTitle": cascadeTitle,
		"Back":         previewBack[req.Type],
	})
}

// isInstallerType 报告镜像类型是否为安装包（无依赖概念，级联语义为
// 版本组）。
//
//	@param typ string 镜像类型。
//	@return bool 安装包类型时为 true。
func isInstallerType(typ string) bool {
	return typ == "python" || typ == "node" || typ == "golang"
}

// planAndDelete 校验删除请求、构建清单（含级联依赖）并执行（或仅预览）。
//
//	@param req deleteRequest 删除请求。
//	@return deleteResult 预览或执行结果清单。
//	@return int 错误时的 HTTP 状态码，成功时忽略。
//	@return error 参数非法、目标不存在或部分文件删除失败的原因。
func (s *Server) planAndDelete(req deleteRequest) (deleteResult, int, error) {
	switch req.Type {
	case "pypi", "npm", "gomod":
		// 依赖类型，支持 --deps 级联
	case "python", "node", "golang":
		// 安装包仅在按文件名删除时 --deps 有效（扩展为整版本组）；
		// 按版本删除本身已是整组
		if req.Deps && !installerFileArg(req.Type, req.Name) {
			return deleteResult{}, http.StatusBadRequest, fmt.Errorf("%s 按版本删除已是整组，--deps 仅用于按文件名删除时扩展为整版本组", req.Type)
		}
	default:
		return deleteResult{}, http.StatusBadRequest, fmt.Errorf("type 必须是 pypi、python、npm、node、golang 或 gomod")
	}
	targets, status, err := s.collectTargets(req.Type, req.Name)
	if err != nil {
		return deleteResult{}, status, err
	}
	var cascades []deleteAction
	if req.Deps {
		if cascades, err = s.collectCascades(req.Type, req.Name); err != nil {
			return deleteResult{}, http.StatusInternalServerError, err
		}
	}
	res := deleteResult{Status: "preview", Targets: []deleteUnit{}, Cascades: []deleteUnit{}}
	if !req.DryRun {
		res.Status = "ok"
	}
	for _, a := range targets {
		res.Targets = append(res.Targets, a.unit)
		res.Count += len(a.unit.Files)
		res.Total += a.unit.Size
	}
	for _, a := range cascades {
		res.Cascades = append(res.Cascades, a.unit)
		res.Count += len(a.unit.Files)
		res.Total += a.unit.Size
	}
	if req.DryRun {
		return res, 0, nil
	}
	var failures []string
	for _, a := range append(targets, cascades...) {
		if err := a.remove(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", a.unit.Name, err))
		}
	}
	if len(failures) > 0 {
		return res, http.StatusInternalServerError, fmt.Errorf("以下 %d 项删除失败：\n%s", len(failures), strings.Join(failures, "\n"))
	}
	return res, 0, nil
}

// collectTargets 按类型收集删除目标单元；目标不存在返回 404。
//
//	@param typ string 镜像类型。
//	@param name string 删除目标（包名 / 模块路径 / 版本参数）。
//	@return []deleteAction 目标单元清单（安装包类型可为多个版本目录/文件组）。
//	@return int 错误时的 HTTP 状态码。
//	@return error 参数非法或目标不存在的原因。
func (s *Server) collectTargets(typ, name string) ([]deleteAction, int, error) {
	switch typ {
	case "pypi":
		name = pypi.NormalizeName(name)
		if !nameRe.MatchString(name) {
			return nil, http.StatusBadRequest, fmt.Errorf("pypi 包名包含非法字符")
		}
		return s.dirActions(typ, name, filepath.Join("pypi", name), nil)
	case "npm":
		if !npm.ValidName(name) {
			return nil, http.StatusBadRequest, fmt.Errorf("npm 包名包含非法字符")
		}
		return s.dirActions(typ, name, filepath.Join("npm", filepath.FromSlash(name)), s.npmScopeCleanup(name))
	case "gomod":
		if !gomod.ValidModulePath(name) {
			return nil, http.StatusBadRequest, fmt.Errorf("gomod 模块路径包含非法字符")
		}
		return s.dirActions(typ, name, filepath.Join("gomod", filepath.FromSlash(name)), s.gomodParentCleanup(name))
	case "python":
		return s.pythonActions(name)
	case "node":
		return s.nodeActions(name)
	case "golang":
		return s.golangActions(name)
	default:
		return nil, http.StatusBadRequest, fmt.Errorf("未知类型 %q", typ)
	}
}

// dirActions 构建目录型删除单元：目录下全部文件（含 .sha256 与 .part
// 临时文件）计入清单，执行动作为整目录 RemoveAll。
//
//	@param typ string 镜像类型（错误信息用）。
//	@param name string 展示名（错误信息用）。
//	@param relDir string 相对 root 的目标目录（已过白名单校验）。
//	@param cleanup func() 删除成功后追加执行的清理动作（空父目录），可为 nil。
//	@return []deleteAction 单元素目标单元。
//	@return int 错误时的 HTTP 状态码。
//	@return error 目录不存在时的错误。
func (s *Server) dirActions(typ, name, relDir string, cleanup func()) ([]deleteAction, int, error) {
	abs := filepath.Join(s.cfg.Root, relDir)
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return nil, http.StatusNotFound, fmt.Errorf("%s %s 不存在", typ, name)
	}
	unit := deleteUnit{Name: name}
	// 统计失败的文件不计入清单，但 RemoveAll 仍会尝试删除，
	// 因此预览文件数和大小可能与实际删除结果有差异。
	_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			if rel, e := filepath.Rel(s.cfg.Root, p); e == nil {
				unit.Files = append(unit.Files, deleteFile{Path: filepath.ToSlash(rel), Size: info.Size()})
				unit.Size += info.Size()
			}
		}
		return nil
	})
	return []deleteAction{{unit: unit, remove: func() error {
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
		if cleanup != nil {
			cleanup()
		}
		return nil
	}}}, 0, nil
}

// fileActions 构建文件型删除单元：files 为绝对路径列表（应含伴随
// .sha256），执行时逐文件删除，全删后清理 dirs 中变空的目录。
//
//	@param typ string 镜像类型（错误信息用）。
//	@param name string 展示名（错误信息与清单用）。
//	@param files []string 待删文件的绝对路径。
//	@param dirs []string 删后需清理的空目录（绝对路径，重复自动去重）。
//	@return []deleteAction 单元素目标单元。
//	@return int 错误时的 HTTP 状态码。
//	@return error files 为空时的错误。
func (s *Server) fileActions(typ, name string, files, dirs []string) ([]deleteAction, int, error) {
	if len(files) == 0 {
		return nil, http.StatusNotFound, fmt.Errorf("%s %s 不存在", typ, name)
	}
	unit := deleteUnit{Name: name}
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if rel, e := filepath.Rel(s.cfg.Root, f); e == nil {
			unit.Files = append(unit.Files, deleteFile{Path: filepath.ToSlash(rel), Size: info.Size()})
			unit.Size += info.Size()
		}
	}
	seen := map[string]bool{}
	return []deleteAction{{unit: unit, remove: func() error {
		for _, f := range files {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		for _, d := range dirs {
			if seen[d] {
				continue
			}
			seen[d] = true
			_ = os.Remove(d) // 非空即失败，忽略
		}
		return nil
	}}}, 0, nil
}

// pyVerRe 是 python 删除目标的版本参数（3.12.15 精确、3.12 前缀；t 为
// free-threaded 构建变体）。
var pyVerRe = regexp.MustCompile(`^\d+\.\d+(\.\d+)?t?$`)

// nodeVerArgRe 是 node 删除目标的版本目录参数（v22.14.0 精确、v22 前缀）。
var nodeVerArgRe = regexp.MustCompile(`^v\d+\.\d+(\.\d+)?$`)

// goVerArgRe 是 golang 删除目标的版本参数（1.23.4 精确、1.23 前缀）。
var goVerArgRe = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// versionArgMatch 判断完整版本 ver 是否命中删除参数 arg：精确相等，或
// arg 为两段形式（3.12 / v22 / 1.23）时前缀匹配该系列全部补丁版。
//
//	@param arg string 删除请求中的版本参数。
//	@param ver string 文件名 / 目录名解析出的完整版本。
//	@return bool 命中时为 true。
func versionArgMatch(arg, ver string) bool {
	if arg == ver {
		return true
	}
	return strings.Count(arg, ".") == 1 && strings.HasPrefix(ver, arg+".")
}

// installerFileArg 报告安装包类型的删除目标是否为文件名形态（行级单
// 文件删除，--deps 可扩展为整版本组）。归档文件名前缀用于校验：
// node 的 SHASUMS256.txt 不以 node-v 开头，不能作为删除目标
// （删它会断掉同版本剩余归档的校验）。
//
//	@param typ string 镜像类型（python/node/golang）。
//	@param name string 删除目标。
//	@return bool 文件名形态时为 true。
func installerFileArg(typ, name string) bool {
	switch typ {
	case "python":
		return strings.HasPrefix(name, "cpython-") && fileRe.MatchString(name)
	case "node":
		return strings.HasPrefix(name, "node-v") && fileRe.MatchString(name)
	case "golang":
		// 以归档扩展名区分：go1.23.4 是版本参数，go1.23.4.linux-amd64.tar.gz
		// 才是文件名（官方归档仅 .tar.gz 与 .zip 两种扩展）
		return strings.HasPrefix(name, "go") && fileRe.MatchString(name) &&
			golang.VersionOf(name) != "" &&
			(strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip"))
	}
	return false
}

// nodeVersionOfFilename 从 node 归档文件名提取版本目录名
// （node-v22.14.0-linux-x64.tar.xz → v22.14.0）；无法解析时为空串。
//
//	@param filename string 归档文件名。
//	@return string 版本目录名（vX.Y.Z）。
func nodeVersionOfFilename(filename string) string {
	m := nodeFileVerRe.FindStringSubmatch(filename)
	if m == nil {
		return ""
	}
	return m[1]
}

// nodeFileVerRe 匹配 node 归档文件名的版本段。
var nodeFileVerRe = regexp.MustCompile(`^node-(v\d+\.\d+\.\d+)-`)

// pythonVersionFiles 收集匹配版本参数的全部 python 安装包文件（跨构建
// tag，含自然命中的 .sha256 伴随文件）与涉及的 tag 目录。
//
//	@param arg string 版本参数（3.12.15 精确或 3.12 前缀）。
//	@return []string 文件绝对路径列表。
//	@return []string 涉及的 tag 目录（删后需清理空目录）。
func (s *Server) pythonVersionFiles(arg string) (files, dirs []string) {
	tags, _ := s.listPythonTags()
	for _, tag := range tags {
		dir := filepath.Join(s.cfg.Root, "python", tag)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			// .sha256 伴随文件解析出相同版本会自然命中，无需单独补录
			if e.IsDir() || strings.HasPrefix(e.Name(), ".part-") || !versionArgMatch(arg, pythonVersionOf(e.Name())) {
				continue
			}
			files = append(files, filepath.Join(dir, e.Name()))
			dirs = append(dirs, dir)
		}
	}
	return files, dirs
}

// nodeVersionFiles 收集版本目录内的全部文件（含 SHASUMS256.txt 与伴随
// .sha256，跳过 .part- 临时文件），供版本组删除与级联差集计算。
//
//	@param ver string 版本目录名（vX.Y.Z）。
//	@return []string 文件绝对路径列表；目录不存在时为空。
//	@return string 版本目录绝对路径。
func (s *Server) nodeVersionFiles(ver string) ([]string, string) {
	dir := filepath.Join(s.cfg.Root, "node", ver)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, dir
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".part-") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	return files, dir
}

// golangVersionFiles 收集版本组的全部归档文件（含自然命中的 .sha256）。
//
//	@param arg string 版本参数（1.23.4 精确或 1.23 前缀，无 go 前缀）。
//	@return []string 文件绝对路径列表。
func (s *Server) golangVersionFiles(arg string) []string {
	entries, err := os.ReadDir(filepath.Join(s.cfg.Root, "golang"))
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".part-") || !versionArgMatch("go"+arg, golang.VersionOf(e.Name())) {
			continue
		}
		files = append(files, filepath.Join(s.cfg.Root, "golang", e.Name()))
	}
	return files
}

// pythonActions 收集删除目标：文件名形态为单文件（含伴随 .sha256），
// 版本参数形态为跨 tag 的版本组。
//
//	@param arg string 文件名或版本参数（3.12.15 / 3.12）。
//	@return []deleteAction 目标单元。
//	@return int 错误时的 HTTP 状态码。
//	@return error 参数非法或目标不存在时的错误。
func (s *Server) pythonActions(arg string) ([]deleteAction, int, error) {
	if installerFileArg("python", arg) {
		files, dirs, err := s.installerFile("python", arg)
		if err != nil {
			return nil, http.StatusNotFound, err
		}
		return s.fileActions("python", arg, files, dirs)
	}
	if !pyVerRe.MatchString(arg) {
		return nil, http.StatusBadRequest, fmt.Errorf("python 删除目标须是版本（如 3.12 或 3.12.15）或归档文件名")
	}
	files, dirs := s.pythonVersionFiles(arg)
	return s.fileActions("python", arg, files, dirs)
}

// nodeActions 收集删除目标：文件名形态为单归档（SHASUMS256.txt 按共享
// 规则处理），版本参数形态为版本目录（v22 前缀可命中多个）。
//
//	@param arg string 文件名或版本参数（v22.14.0 / v22）。
//	@return []deleteAction 目标单元。
//	@return int 错误时的 HTTP 状态码。
//	@return error 参数非法或目标不存在时的错误。
func (s *Server) nodeActions(arg string) ([]deleteAction, int, error) {
	if installerFileArg("node", arg) {
		files, dirs, err := s.nodeFileTargets(arg)
		if err != nil {
			return nil, http.StatusNotFound, err
		}
		return s.fileActions("node", arg, files, dirs)
	}
	if !nodeVerArgRe.MatchString(arg) {
		return nil, http.StatusBadRequest, fmt.Errorf("node 删除目标须是版本（如 v22 或 v22.14.0）或归档文件名")
	}
	vers, _ := s.listNodeVersions()
	var acts []deleteAction
	for _, ver := range vers {
		if !versionArgMatch(arg, ver) {
			continue
		}
		act, _, err := s.dirActions("node", ver, filepath.Join("node", ver), nil)
		if err != nil {
			continue
		}
		acts = append(acts, act...)
	}
	if len(acts) == 0 {
		return nil, http.StatusNotFound, fmt.Errorf("node %s 不存在", arg)
	}
	return acts, 0, nil
}

// golangActions 收集删除目标：文件名形态为单归档（含伴随 .sha256），
// 版本参数形态为版本文件组。
//
//	@param arg string 文件名或版本参数（1.23.4 / 1.23，兼容 go 前缀）。
//	@return []deleteAction 目标单元。
//	@return int 错误时的 HTTP 状态码。
//	@return error 参数非法或目标不存在时的错误。
func (s *Server) golangActions(arg string) ([]deleteAction, int, error) {
	if installerFileArg("golang", arg) {
		files, dirs, err := s.installerFile("golang", arg)
		if err != nil {
			return nil, http.StatusNotFound, err
		}
		return s.fileActions("golang", arg, files, dirs)
	}
	// 版本参数接受 go 前缀，如 go1.23.4。
	arg = strings.TrimPrefix(arg, "go")
	if !goVerArgRe.MatchString(arg) {
		return nil, http.StatusBadRequest, fmt.Errorf("golang 删除目标须是版本（如 1.23 或 1.23.4）或归档文件名")
	}
	return s.fileActions("golang", arg, s.golangVersionFiles(arg), nil)
}

// installerFile 定位 python / golang 的单个归档文件并附上伴随 .sha256
// （镜像自生成缓存，与主文件一一对应，随主文件删）。
//
//	@param typ string 镜像类型（python 或 golang）。
//	@param name string 归档文件名。
//	@return []string 待删文件绝对路径（主文件 + 存在的 .sha256）。
//	@return []string 删后需清理的空目录。
//	@return error 文件不存在时的错误。
func (s *Server) installerFile(typ, name string) ([]string, []string, error) {
	dir := filepath.Join(s.cfg.Root, typ)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s 不存在", typ, name)
	}
	// python 归档在 tag 子目录下，golang 平铺在根下
	if typ == "python" {
		for _, tag := range entries {
			if !tag.IsDir() {
				continue
			}
			files, dirs, err := s.installerFileIn(filepath.Join(dir, tag.Name()), name)
			if err == nil {
				return files, dirs, nil
			}
		}
		return nil, nil, fmt.Errorf("python %s 不存在", name)
	}
	return s.installerFileIn(dir, name)
}

// installerFileIn 在单个目录内定位归档文件并附伴随 .sha256。
//
//	@param dir string 归档所在目录。
//	@param name string 归档文件名。
//	@return []string 待删文件绝对路径。
//	@return []string 需清理的空目录（该目录本身）。
//	@return error 文件不存在时的错误。
func (s *Server) installerFileIn(dir, name string) ([]string, []string, error) {
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return nil, nil, err
	}
	files := []string{filepath.Join(dir, name)}
	if _, err := os.Stat(filepath.Join(dir, name+".sha256")); err == nil {
		files = append(files, filepath.Join(dir, name+".sha256"))
	}
	return files, []string{dir}, nil
}

// nodeFileTargets 定位单个 node 归档并按共享规则处理 SHASUMS256.txt：
// 官方原样文件、版本内全部归档共享，仅当本归档删除后目录内再无其他
// 归档时才一并删除（残留行对校验无副作用，不改写官方文件）。
//
//	@param name string 归档文件名（node-v 开头）。
//	@return []string 待删文件绝对路径。
//	@return []string 需清理的空目录（归档清零时的版本目录）。
//	@return error 文件不存在时的错误。
func (s *Server) nodeFileTargets(name string) ([]string, []string, error) {
	ver := nodeVersionOfFilename(name)
	dir := filepath.Join(s.cfg.Root, "node", ver)
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		return nil, nil, fmt.Errorf("node %s 不存在", name)
	}
	files := []string{filepath.Join(dir, name)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	hasOther := false
	for _, e := range entries {
		if e.IsDir() || e.Name() == name || e.Name() == "SHASUMS256.txt" ||
			strings.HasSuffix(e.Name(), ".sha256") || strings.HasPrefix(e.Name(), ".part-") {
			continue
		}
		hasOther = true
		break
	}
	if !hasOther {
		if _, err := os.Stat(filepath.Join(dir, "SHASUMS256.txt")); err == nil {
			files = append(files, filepath.Join(dir, "SHASUMS256.txt"))
		}
		return files, []string{dir}, nil
	}
	return files, nil, nil
}

// npmScopeCleanup 返回删除 scoped 包后清理可能变空的 @scope 目录的
// 动作；普通包返回 nil（无父目录需要清理）。
//
//	@param name string 完整 npm 包名。
//	@return func() 清理动作。
func (s *Server) npmScopeCleanup(name string) func() {
	if !strings.HasPrefix(name, "@") {
		return nil
	}
	scopeDir := filepath.Join(s.cfg.Root, "npm", strings.SplitN(name, "/", 2)[0])
	return func() { _ = os.Remove(scopeDir) }
}

// gomodParentCleanup 返回删除模块目录后逐级向上清理空的模块路径父
// 目录的动作（os.Remove 只删空目录，非空失败即停止上溯）。
//
//	@param mod string 原始模块路径。
//	@return func() 清理动作。
func (s *Server) gomodParentCleanup(mod string) func() {
	base := filepath.Join(s.cfg.Root, "gomod")
	return func() {
		dir := filepath.Dir(filepath.Join(base, filepath.FromSlash(mod)))
		for dir != base && strings.HasPrefix(dir, base+string(filepath.Separator)) {
			if os.Remove(dir) != nil {
				return
			}
			dir = filepath.Dir(dir)
		}
	}
}

// installerCascadeActions 构建安装包文件名形态的级联单元：所属版本组
// 全部文件减去目标文件（python / golang 的伴随 .sha256 与目标一并排除；
// node 的 SHASUMS256.txt 属于组文件，保留在差集中——级联删除时整组
// 全删），差集逐文件删除并清理空目录。
//
//	@param typ string 镜像类型（python/node/golang）。
//	@param name string 归档文件名。
//	@return []deleteAction 级联单元（差集为空时为 nil）。
//	@return error 版本组收集失败的原因。
func (s *Server) installerCascadeActions(typ, name string) ([]deleteAction, error) {
	var groupFiles []string
	var dirs []string
	desc := ""
	switch typ {
	case "python":
		ver := pythonVersionOf(name)
		groupFiles, dirs = s.pythonVersionFiles(ver)
		desc = ver
	case "node":
		ver := nodeVersionOfFilename(name)
		files, dir := s.nodeVersionFiles(ver)
		groupFiles, dirs = files, []string{dir}
		desc = ver
	case "golang":
		ver := strings.TrimPrefix(golang.VersionOf(name), "go")
		groupFiles = s.golangVersionFiles(ver)
		desc = golang.VersionOf(name)
	}
	// 目标文件与其伴随 .sha256 不进差集（已在目标单元中）
	exclude := map[string]bool{}
	for _, base := range []string{name, name + ".sha256"} {
		for _, f := range groupFiles {
			if filepath.Base(f) == base {
				exclude[f] = true
			}
		}
	}
	var diff []string
	for _, f := range groupFiles {
		if !exclude[f] {
			diff = append(diff, f)
		}
	}
	if len(diff) == 0 {
		return nil, nil
	}
	act, _, err := s.fileActions(typ, desc, diff, dirs)
	if err != nil {
		return nil, err
	}
	return act, nil
}

// collectCascades 计算并收集级联删除单元：目标删除后不再被任何幸存包
// 引用的传递依赖；安装包文件名目标收集同版本其余文件。
//
//	@param typ string 镜像类型，支持三种包/模块类型和三种安装包类型。
//	@param target string 规范包名、模块路径或安装包文件名。
//	@return []deleteAction 级联单元列表。
//	@return error 建图或单元收集失败的原因。
func (s *Server) collectCascades(typ, target string) ([]deleteAction, error) {
	var graph map[string][]string
	var all []string
	switch typ {
	case "pypi":
		graph, all = s.pypiDepGraph()
	case "npm":
		graph, all = s.npmDepGraph()
	case "gomod":
		graph, all = s.gomodDepGraph()
	case "python", "node", "golang":
		// 安装包文件名形态：级联 = 所属版本组减目标（版本形态不会走到
		// 这里，planAndDelete 已拒绝）
		return s.installerCascadeActions(typ, target)
	default:
		return nil, nil
	}
	var acts []deleteAction
	for _, v := range cascadeVictims(graph, all, target) {
		act, _, err := s.collectTargets(typ, v)
		if err != nil {
			return nil, fmt.Errorf("收集级联目标 %s 失败: %w", v, err)
		}
		acts = append(acts, act...)
	}
	return acts, nil
}

// cascadeVictims 计算删除 target 后可一并删除的传递依赖：
//
//	victims = closure(target) − reachable(roots)
//	closure(target) = target 沿依赖边的传递闭包（依赖的依赖）
//	roots = 全部包 − {target} − closure(target)（幸存根）
//	reachable(roots) = roots 自身及其传递可达
//
// 被幸存包引用的依赖保留，包括其中的环；只被目标引用的依赖环可删除。
// 图不含指向镜像外包的边。
//
//	@param graph map[string][]string 依赖边（包 → 依赖包列表）。
//	@param all []string 全部包名（图的节点集）。
//	@param target string 删除目标。
//	@return []string 排序后的级联删除包名列表。
func cascadeVictims(graph map[string][]string, all []string, target string) []string {
	closure := reachSet(graph, []string{target}, target)
	roots := make([]string, 0, len(all))
	for _, p := range all {
		if p != target && !closure[p] {
			roots = append(roots, p)
		}
	}
	keep := reachSet(graph, roots, "")
	var victims []string
	for p := range closure {
		if !keep[p] {
			victims = append(victims, p)
		}
	}
	sort.Strings(victims)
	return victims
}

// reachSet 返回 seeds 沿依赖边可达的节点集合，并移除 exclude。
//
//	@param graph map[string][]string 依赖边。
//	@param seeds []string 遍历起点；起点仅在沿边再次到达时计入结果。
//	@param exclude string 不计入结果但仍沿其边遍历的节点（target 场景
//	 传 target：target 成环自依赖时不把自己算进闭包）。
//	@return map[string]bool 可达集合。
func reachSet(graph map[string][]string, seeds []string, exclude string) map[string]bool {
	seen := map[string]bool{}
	var stack []string
	for _, p := range seeds {
		stack = append(stack, p)
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, d := range graph[p] {
			if seen[d] {
				continue
			}
			seen[d] = true
			stack = append(stack, d)
		}
	}
	if exclude != "" {
		delete(seen, exclude)
	}
	return seen
}

// pypiDepGraph 构建当前镜像的 PyPI 依赖图：每个包的全部发行文件解析
// Requires-Dist 的并集作为该包的依赖边。读取或解析失败的文件跳过，
// 边只保留指向镜像内已存在包的部分。
//
//	@return map[string][]string 依赖边（规范包名 → 依赖包名列表）。
//	@return []string 全部包名。
func (s *Server) pypiDepGraph() (map[string][]string, []string) {
	pkgs, _ := s.listPackages()
	graph := make(map[string][]string, len(pkgs))
	for _, name := range pkgs {
		graph[name] = nil
	}
	for _, name := range pkgs {
		files, err := s.listDistFiles(name)
		if err != nil {
			continue
		}
		deps := map[string]bool{}
		for _, f := range files {
			names, err := pypi.DepNamesFromArchive(filepath.Join(s.cfg.Root, "pypi", name, f.Name))
			if err != nil {
				continue
			}
			for _, d := range names {
				deps[d] = true
			}
		}
		for d := range deps {
			if _, ok := graph[d]; ok {
				graph[name] = append(graph[name], d)
			}
		}
		sort.Strings(graph[name])
	}
	return graph, pkgs
}

// npmDepGraph 构建当前镜像的 npm 依赖图：每包读 packument.json，取已
// 镜像 tarball 对应版本的依赖并集作为边。
//
//	@return map[string][]string 依赖边（完整包名 → 依赖包名列表）。
//	@return []string 全部包名。
func (s *Server) npmDepGraph() (map[string][]string, []string) {
	rows, _ := s.listNpmPackages()
	graph := make(map[string][]string, len(rows))
	for _, row := range rows {
		graph[row.Name] = nil
	}
	for _, row := range rows {
		dir := filepath.Join(s.cfg.Root, "npm", filepath.FromSlash(row.Name))
		tarballs := map[string]bool{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".tgz") {
				tarballs[e.Name()] = true
			}
		}
		body, err := os.ReadFile(filepath.Join(dir, "packument.json"))
		if err != nil {
			continue
		}
		names, err := npm.DepNamesForTarballs(body, func(f string) bool { return tarballs[f] })
		if err != nil {
			continue
		}
		for _, d := range names {
			if _, ok := graph[d]; ok {
				graph[row.Name] = append(graph[row.Name], d)
			}
		}
	}
	return graph, namesOf(rows)
}

// namesOf 提取 npm 包行数据的包名列表。
func namesOf(rows []npmPkgRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

// gomodDepGraph 构建当前镜像的 Go 模块依赖图：每模块读 @v 下全部 .mod
// 的 require 声明并集作为边，使用 gomod.ParseRequires 解析。
//
//	@return map[string][]string 依赖边（模块路径 → 依赖模块路径列表）。
//	@return []string 全部模块路径。
func (s *Server) gomodDepGraph() (map[string][]string, []string) {
	rows, _ := s.listGoModules()
	graph := make(map[string][]string, len(rows))
	for _, row := range rows {
		graph[row.Name] = nil
	}
	for _, row := range rows {
		vdir := filepath.Join(s.cfg.Root, "gomod", filepath.FromSlash(row.Name), "@v")
		entries, err := os.ReadDir(vdir)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		var deps []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".mod") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(vdir, e.Name()))
			if err != nil {
				continue
			}
			for _, mv := range gomod.ParseRequires(string(body)) {
				if _, ok := graph[mv.Module]; ok && !seen[mv.Module] {
					seen[mv.Module] = true
					deps = append(deps, mv.Module)
				}
			}
		}
		sort.Strings(deps)
		graph[row.Name] = deps
	}
	all := make([]string, len(rows))
	for i, r := range rows {
		all[i] = r.Name
	}
	return graph, all
}
