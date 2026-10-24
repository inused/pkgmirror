package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// DeleteOptions 是 delete 子命令的全部输入。
type DeleteOptions struct {
	// Type 是镜像类型：pypi/python/npm/node/golang/gomod。
	Type string
	// Name 是删除目标：包名、模块路径、安装包归档文件名或版本。
	// 版本支持系列前缀，如 python 3.12、node v22、golang 1.23。
	Name string
	// Deps 为 true 时级联依赖或安装包版本组；false 只删除指定目标。
	Deps bool
	// Yes 为 true 时跳过交互确认（脚本场景）。
	Yes bool
	// Server 是服务端基础地址，可内嵌凭据（http://user:pass@host）。
	Server string
	// User 是 Basic auth 用户名（Server 已内嵌凭据时可省略）。
	User string
	// Pass 是 Basic auth 密码。
	Pass string
}

// deleteClientTimeout 覆盖单次删除请求（含级联建图）的最长时间。
const deleteClientTimeout = time.Minute

// deleteUnitJSON 与 deleteFileJSON 是服务端响应清单单元的客户端镜像。
type deleteFileJSON struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

type deleteUnitJSON struct {
	Name  string           `json:"name"`
	Files []deleteFileJSON `json:"files"`
	Size  int64            `json:"size"`
}

type deleteResultJSON struct {
	Status   string           `json:"status"`
	Targets  []deleteUnitJSON `json:"targets"`
	Cascades []deleteUnitJSON `json:"cascades"`
	Count    int              `json:"count"`
	Total    int64            `json:"total"`
}

// RunDelete 预览并删除服务端上的指定资源。
//
// 先以 dry-run 请求打印将删清单（目标与级联依赖的名称、文件数、大小），
// Yes 为 true 跳过确认，否则读 stdin 等待 y 确认；确认后再次请求执行并
// 打印结果。服务端目标不存在（404）等错误原样返回。
//
//	@param opts DeleteOptions 已解析的选项。
//	@return error 请求失败、用户取消或服务端拒绝时的原因。
func RunDelete(opts DeleteOptions) error {
	base, user, pass, err := parseServer(UploadOptions{Server: opts.Server, User: opts.User, Pass: opts.Pass})
	if err != nil {
		return err
	}
	// 删除目标是内网地址，同 upload 显式禁用代理
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	client := &http.Client{Timeout: deleteClientTimeout, Transport: tr}

	preview, err := requestDelete(client, base, user, pass, opts.Type, opts.Name, opts.Deps, true)
	if err != nil {
		return err
	}
	printDeleteUnits("删除目标", preview.Targets)
	if len(preview.Cascades) > 0 {
		printDeleteUnits("级联依赖（不再被其他包引用）", preview.Cascades)
	}
	log.Printf("合计 %d 个文件，%s", preview.Count, humanBytes(preview.Total))
	if !opts.Yes {
		fmt.Print("确认删除？（y/N）")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			return fmt.Errorf("已取消")
		}
	}
	result, err := requestDelete(client, base, user, pass, opts.Type, opts.Name, opts.Deps, false)
	if err != nil {
		return err
	}
	log.Printf("已删除 %d 个文件，释放 %s", result.Count, humanBytes(result.Total))
	return nil
}

// requestDelete 发起一次删除请求并解析响应。
//
//	@param client *http.Client HTTP 客户端。
//	@param base string 服务端基础地址（无凭据）。
//	@param user string Basic auth 用户名，可空。
//	@param pass string Basic auth 密码，可空。
//	@param typ string 镜像类型。
//	@param name string 删除目标。
//	@param deps bool 是否级联删除依赖项。
//	@param dryRun bool true 只取清单不执行。
//	@return deleteResultJSON 服务端响应清单。
//	@return error 网络错误或服务端返回非 2xx 时的原因（含服务端错误文本）。
func requestDelete(client *http.Client, base, user, pass, typ, name string, deps, dryRun bool) (deleteResultJSON, error) {
	var res deleteResultJSON
	body, err := json.Marshal(deleteRequestJSON{Type: typ, Name: name, Deps: deps, DryRun: dryRun})
	if err != nil {
		return res, err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/delete", bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	// 成功响应是完整删除清单，级联大包时可到 MB 级，不做长度限制
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return res, fmt.Errorf("读取响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return res, fmt.Errorf("服务端返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if err := json.Unmarshal(respBody, &res); err != nil {
		return res, fmt.Errorf("解析服务端响应失败: %w", err)
	}
	return res, nil
}

// deleteRequestJSON 是 /api/delete 的 JSON 请求体。
type deleteRequestJSON struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Deps   bool   `json:"deps"`
	DryRun bool   `json:"dryRun"`
}

// printDeleteUnits 打印一组删除单元（预览清单）。
//
//	@param title string 组标题。
//	@param units []deleteUnitJSON 单元列表。
func printDeleteUnits(title string, units []deleteUnitJSON) {
	log.Printf("%s:", title)
	for _, u := range units {
		log.Printf("  %s  %d 个文件  %s", u.Name, len(u.Files), humanBytes(u.Size))
	}
}

// humanBytes 把字节数换算为人类可读格式（客户端日志用）。
//
//	@param n int64 字节数。
//	@return string 带单位的可读大小（如 1.5M）；小于 1024 时原样带 B。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
