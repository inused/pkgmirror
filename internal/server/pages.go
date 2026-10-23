package server

import (
	"fmt"
	"html/template"
	"net/http"
)

// humanSize 将字节数换算为人类可读格式。
//
// 小于 1024 时显示字节数（如 512B），否则择最大匹配单位并保留一位小数
// （如 1.5K、24.7M、1.2G）。
//
//	@param n int64 字节数。
//	@return string 带单位的可读大小。
func humanSize(n int64) string {
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

// tmplFuncs 是全部页面模板共用的自定义函数。
var tmplFuncs = template.FuncMap{"humanSize": humanSize}

// 每个页面一个独立模板，共享同一段内嵌样式。模板经 html/template 渲染，
// 填充值（包名、文件名等）即便含特殊字符也会被转义。
// 样式只用广泛支持的基础属性（不用 CSS 变量与 grid），兼容老旧浏览器。

// pageCSS 是全部页面共享的样式。
const pageCSS = `<style>
body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif;background:#f5f6f8;color:#24292f;margin:0;padding:2rem 1rem}
.container{max-width:1280px;margin:0 auto}
h1{margin:0 0 .4rem;font-size:1.5rem}
.sub{color:#57606a;margin:0 0 1.2rem}
nav{background:#fff;border:1px solid #e1e4e8;border-radius:8px;padding:.5rem .75rem;margin-bottom:1.5rem}
nav a{color:#0969da;text-decoration:none;padding:.35rem .7rem;border-radius:6px;display:inline-block}
nav a:hover{background:#eaf2fc}
nav b{color:#57606a;font-size:.95rem;padding:.35rem .7rem;display:inline-block}
table{background:#fff;border:1px solid #e1e4e8;border-radius:8px;border-collapse:separate;border-spacing:0;width:100%;margin:1rem 0}
th,td{padding:.5rem .8rem;border-bottom:1px solid #eaecef;text-align:left;word-break:break-all}
th{background:#f6f8fa;font-weight:600}
tr:last-child td{border-bottom:none}
tbody tr:hover{background:#f6f8fa}
code,pre{font-family:ui-monospace,SFMono-Regular,Consolas,"Courier New",monospace}
code{background:#f6f8fa;border-radius:4px;padding:.08rem .35rem}
pre{background:#f6f8fa;border:1px solid #e1e4e8;border-radius:8px;padding:.8rem 1rem;overflow-x:auto;margin:.5rem 0}
.stats{margin:1.2rem 0}
.stat{background:#fff;border:1px solid #e1e4e8;border-radius:8px;padding:.9rem 1.2rem;display:inline-block;margin:0 1rem 1rem 0}
.stat b{display:block;font-size:1.6rem}
.stat span{color:#57606a}
.cfg{background:#fff;border:1px solid #e1e4e8;border-radius:8px;padding:1rem 1.2rem;margin:1rem 0}
.cfg h2{margin:.2rem 0 .6rem;font-size:1.05rem}
.copy{background:#f6f8fa;border:1px solid #d0d7de;border-radius:6px;padding:.25rem .7rem;font-size:.85rem;color:#24292f;cursor:pointer}
.copy:hover{background:#eef1f4}
.ok{color:#1a7f37;background:#dafbe1;border-radius:6px;padding:.5rem .8rem;display:inline-block;margin:.5rem 0}
form{background:#fff;border:1px solid #e1e4e8;border-radius:8px;padding:1rem 1.2rem;margin-top:1rem}
label{display:block;font-weight:600;margin:.7rem 0 .25rem}
input[type=text],select{padding:.45rem .6rem;border:1px solid #d0d7de;border-radius:6px;width:100%;max-width:420px;box-sizing:border-box;font-size:.95rem}
input[type=file]{margin-top:.3rem}
button[type=submit]{background:#1f883d;color:#fff;border:none;border-radius:6px;padding:.5rem 1.3rem;font-size:.95rem;margin-top:1rem;cursor:pointer}
button[type=submit]:hover{background:#1a7f37}
.hint{color:#57606a}
.cfgbar{background:#fff;border:1px solid #e1e4e8;border-radius:8px;padding:.6rem 1rem;margin:0 0 1rem;font-size:.9rem;line-height:1.9}
.cfgbar code{font-size:.85rem}
</style>`

// navHead 是人用页面的容器开头与导航条，各页面以 </div></body></html> 收尾。
const navHead = `<div class="container">
<nav><b>pymirror</b><a href="/">首页</a><a href="/browse/">PyPI 包</a><a href="/python/">Python 安装包</a><a href="/upload">上传</a></nav>`

// copyScript 是配置块的复制按钮脚本；浏览器不支持时退化为提示手动复制。
const copyScript = `<script>
function copyCmd(id,btn){
  var t=document.getElementById(id).textContent;
  if(navigator.clipboard&&navigator.clipboard.writeText){
    navigator.clipboard.writeText(t).then(function(){btn.textContent='已复制';setTimeout(function(){btn.textContent='复制'},1500)});
  }else{
    btn.textContent='请手动选择复制';setTimeout(function(){btn.textContent='复制'},1500);
  }
}
</script>`

// Host 字段来自请求的 Host 头，页面用它拼出可直接复制的接入地址，
// 用户访问用的是什么地址，页面上显示的就是什么地址。
var indexTpl = template.Must(template.New("index").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>pymirror</title>` + pageCSS + `</head><body>
<div class="container" style="margin-bottom:1.5rem">
<h1>pymirror</h1>
<p class="sub">PyPI / uv Python 离线镜像</p>
</div>
` + navHead + `
<div class="stats">
<div class="stat"><b>{{.Packages}}</b><span>个包</span></div>
<div class="stat"><b>{{.Tags}}</b><span>个 Python 构建标签</span></div>
</div>
<div class="cfg">
<h2>uv 接入（环境变量，把 user:pass 换成本服务账号）</h2>
<pre id="c1">export UV_INDEX_URL=http://user:pass@{{.Host}}/simple
export UV_PYTHON_INSTALL_MIRROR=http://user:pass@{{.Host}}/python</pre>
<button class="copy" onclick="copyCmd('c1',this)">复制</button>
</div>
<div class="cfg">
<h2>pip 接入（把 user:pass 换成本服务账号）</h2>
<pre id="c2">pip install --index-url http://user:pass@{{.Host}}/simple 包名</pre>
<button class="copy" onclick="copyCmd('c2',this)">复制</button>
</div>
</div>
` + copyScript + `
</body></html>`))

var browseTpl = template.Must(template.New("browse").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>包列表 - pymirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>PyPI 包列表</h1>
<div class="cfgbar">pip：<code>pip install --index-url http://user:pass@{{.Host}}/simple 包名</code><br/>
uv：<code>export UV_INDEX_URL=http://user:pass@{{.Host}}/simple</code></div>
{{if .Rows}}<table><thead><tr><th>包名</th><th>文件数</th><th>总大小</th></tr></thead><tbody>
{{range .Rows}}<tr><td><a href="/browse/{{.Name}}/">{{.Name}}</a></td><td>{{.Count}}</td><td>{{humanSize .Size}}</td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无包。请先执行 <code>pymirror download pypi</code> 下载并上传。</p>{{end}}
</div></body></html>`))

var browsePkgTpl = template.Must(template.New("browsePkg").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>{{.Name}} - PyPI 包 - pymirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>{{.Name}}</h1>
<table><thead><tr><th>文件</th><th>大小</th><th>sha256</th></tr></thead><tbody>
{{range .Files}}<tr><td><a href="/pypi/{{$.Name}}/{{.Name}}">{{.Name}}</a></td><td>{{humanSize .Size}}</td><td><code>{{.SHA256}}</code></td></tr>{{end}}
</tbody></table>
</div></body></html>`))

var pythonTpl = template.Must(template.New("python").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Python 安装包 - pymirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>Python 安装包</h1>
<div class="cfgbar">uv：<code>export UV_PYTHON_INSTALL_MIRROR=http://user:pass@{{.Host}}/python</code></div>
{{if .Rows}}<table><thead><tr><th>构建标签</th><th>Python 版本</th><th>文件</th><th>大小</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{.Tag}}</td><td>{{.Version}}</td><td><a href="/python/{{.Tag}}/{{.Filename}}">{{.Filename}}</a></td><td>{{humanSize .Size}}</td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无 Python 安装包。请先执行 <code>pymirror download python x.y</code> 下载并上传。</p>{{end}}
</div></body></html>`))

var python404Tpl = template.Must(template.New("python404").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>文件未同步 - pymirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>文件未同步</h1>
<p>请求的安装包 <code>{{.Path}}</code> 不在本镜像中。</p>
<p>请先执行 <code>pymirror download python &lt;版本&gt;</code> 下载对应文件，再用 <code>pymirror upload</code> 上传到本服务。</p>
</div></body></html>`))

var uploadTpl = template.Must(template.New("upload").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>上传 - pymirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>上传文件</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<form method="post" action="/api/upload" enctype="multipart/form-data">
<label for="f">文件（wheel / sdist / Python 安装包）</label>
<input type="file" name="file" id="f" required>
<label for="t">类型</label>
<select name="type" id="t"><option value="pypi">pypi 包（wheel/sdist）</option><option value="python">python 安装包</option></select>
<label for="n">包名（pypi）</label>
<input type="text" name="name" id="n" placeholder="留空则从文件名自动解析">
<label for="g">构建标签（python）</label>
<input type="text" name="tag" id="g" placeholder="8 位日期，如 20261021">
<button type="submit">上传</button>
</form>
</div></body></html>`))

// render 用指定模板渲染页面并输出。
//
//	@param w http.ResponseWriter 响应写入器。
//	@param t *template.Template 已解析的页面模板。
//	@param data any 模板数据。
func render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.Execute(w, data); err != nil {
		// 页面前半已写出，无法改状态码，记录错误即可
		http.Error(w, "页面渲染失败: "+err.Error(), http.StatusInternalServerError)
	}
}

// renderBody 与 render 相同，但允许调用方先自行设置状态码（如 404 提示页）。
//
//	@param w http.ResponseWriter 响应写入器。
//	@param t *template.Template 已解析的页面模板。
//	@param data any 模板数据。
func renderBody(w http.ResponseWriter, t *template.Template, data any) {
	if err := t.Execute(w, data); err != nil {
		http.Error(w, "页面渲染失败: "+err.Error(), http.StatusInternalServerError)
	}
}
