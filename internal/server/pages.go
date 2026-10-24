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
nav a.cur{background:#0969da;color:#fff}
nav a.cur:hover{background:#0860c4;color:#fff}
nav b{color:#57606a;font-size:.95rem;padding:.35rem .7rem;display:inline-block}
nav .cut{border-left:1px solid #e1e4e8;margin-left:.55rem}
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
.filterbar{margin:0 0 1rem}
.filterbar .hint{margin-left:.6rem}
form.delform{display:inline;background:none;border:none;padding:0;margin:0}
button.delbtn{background:#fff;border:1px solid #d0d7de;color:#cf222e;border-radius:6px;padding:.15rem .6rem;font-size:.82rem;margin:0;cursor:pointer}
button.delbtn:hover{background:#cf222e;border-color:#cf222e;color:#fff}
button.delmain{background:#cf222e;color:#fff;border:1px solid #cf222e;border-radius:6px;padding:.35rem 1rem;font-size:.9rem;margin:0 0 0 .8rem;vertical-align:middle;cursor:pointer}
button.delmain:hover{background:#a40e26;border-color:#a40e26}
a.cancelbtn{background:#fff;border:1px solid #d0d7de;color:#57606a;border-radius:6px;padding:.3rem .9rem;font-size:1rem;margin-left:.8rem;text-decoration:none;vertical-align:middle}
a.cancelbtn:hover{background:#f6f8fa;color:#24292f}
</style>`

// navCurScript 高亮当前页对应的导航链接（cur 类）。
//
// 匹配规则：当前路径与各链接 href 统一补尾斜杠后做前缀比较，取最长的
// 命中者（/browse/gomod/x 同时命中 /browse/ 与 /browse/gomod/，取长者）；
// 首页仅在路径恰为 / 时命中。className 追加而非覆盖，保留上传链接的
// cut 分隔线类。脚本紧跟 nav 之后执行，无需 DOM 就绪钩子；无 JS 环境
// 仅无高亮，不影响导航。
const navCurScript = `<script>
(function(){
  var links=document.querySelectorAll('nav a'),p=location.pathname;
  if(p.charAt(p.length-1)!=='/')p+='/';
  var best=-1,bestLen=0;
  for(var i=0;i<links.length;i++){
    var h=links[i].getAttribute('href');
    if(h.charAt(h.length-1)!=='/')h+='/';
    if((h==='/'&&p==='/')||(h!=='/'&&p.indexOf(h)===0)){
      if(h.length>bestLen){best=i;bestLen=h.length}
    }
  }
  if(best>=0)links[best].className+=' cur';
})();
</script>`

// navHead 是人用页面的容器开头与导航条，各页面以 </div></body></html> 收尾。
// 导航按编程语言分组：语言本体在前、包仓库在后；组名（b）与独立入口
// （上传）带 cut 类，左侧一道细竖线（颜色同页面边框灰）作组间分隔；
// 末尾内嵌当前页高亮脚本（见 navCurScript）。
const navHead = `<div class="container">
<nav><a href="/">首页</a><b class="cut">Python</b><a href="/browse/python/">安装包</a><a href="/browse/pypi/">PyPI 包</a><b class="cut">Node.js</b><a href="/browse/node/">安装包</a><a href="/browse/npm/">npm 包</a><b class="cut">Go</b><a href="/browse/golang/">安装包</a><a href="/browse/gomod/">Go 模块</a><a class="cut" href="/upload">上传</a></nav>
` + navCurScript

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

// filterScript 是列表页的模糊过滤脚本，配合 #pkg-filter 输入框使用。
// 匹配与排序用内嵌的 uFuzzy（见 /static/ufuzzy.min.js），intraMode:1
// 开启单字符插删容错（打错一个字母仍可命中）；库缺失时退化为子串匹配。
// 整行文本参与匹配（列表页首列不一定是包名），清空输入时按页面原始行序恢复。
const filterScript = `<script src="/static/ufuzzy.min.js"></script>
<script>
(function(){
  var box=document.getElementById('pkg-filter');
  var tbody=document.querySelector('table tbody');
  if(!box||!tbody)return;
  var rows=Array.prototype.slice.call(tbody.rows);
  var texts=rows.map(function(tr){return tr.textContent.toLowerCase()});
  var hint=document.getElementById('pkg-filter-hint');
  var uf=window.uFuzzy?new uFuzzy({intraMode:1}):null;
  function apply(){
    var q=box.value.trim(),hits,i;
    if(!q){
      for(i=0;i<rows.length;i++){rows[i].style.display='';tbody.appendChild(rows[i])}
      if(hint)hint.textContent='';
      return;
    }
    // API 序列（uFuzzy README）：filter → info → sort，info 可能进一步
    // 缩减 filter 结果，最终行 = info.idx[order[i]]
    if(uf){
      var idx=uf.filter(texts,q);
      hits=[];
      if(idx&&idx.length){
        var info=uf.info(idx,texts,q);
        var order=uf.sort(info,texts,q);
        for(i=0;i<order.length;i++)hits.push(info.idx[order[i]]);
      }
    }else{
      var lq=q.toLowerCase();
      hits=texts.reduce(function(a,t,i){if(t.indexOf(lq)>=0)a.push(i);return a},[]);
    }
    for(i=0;i<hits.length;i++)tbody.appendChild(rows[hits[i]]);
    var set={};for(i=0;i<hits.length;i++)set[hits[i]]=1;
    for(i=0;i<rows.length;i++)rows[i].style.display=set[i]?'':'none';
    if(hint)hint.textContent=hits.length?('命中 '+hits.length+' 项'):'无匹配项';
  }
  box.addEventListener('input',apply);
})();
</script>`

// Host 字段来自请求的 Host 头，页面用它拼出可直接复制的接入地址，
// 用户访问用的是什么地址，页面上显示的就是什么地址。
var indexTpl = template.Must(template.New("index").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>pkgmirror</h1>
<p class="sub">PyPI / uv Python / npm / Go 离线镜像</p>
<div class="stats">
<div class="stat"><b>{{.Tags}}</b><span>个 Python 构建标签</span></div>
<div class="stat"><b>{{.Packages}}</b><span>个 PyPI 包</span></div>
<div class="stat"><b>{{.NodeVersions}}</b><span>个 Node 版本</span></div>
<div class="stat"><b>{{.NpmPackages}}</b><span>个 npm 包</span></div>
<div class="stat"><b>{{.GoVersions}}</b><span>个 Go 版本</span></div>
<div class="stat"><b>{{.GoModules}}</b><span>个 Go 模块</span></div>
</div>
<div class="cfg">
<h2>uv 接入（环境变量，把 user:pass 换成本服务账号）</h2>
<pre id="c1">export UV_INDEX_URL=http://user:pass@{{.Host}}/mirror/simple
export UV_PYTHON_INSTALL_MIRROR=http://user:pass@{{.Host}}/mirror/python</pre>
<button class="copy" onclick="copyCmd('c1',this)">复制</button>
</div>
<div class="cfg">
<h2>pip 接入（把 user:pass 换成本服务账号）</h2>
<pre id="c2">pip install --index-url http://user:pass@{{.Host}}/mirror/simple 包名</pre>
<button class="copy" onclick="copyCmd('c2',this)">复制</button>
</div>
<div class="cfg">
<h2>npm / pnpm 接入（写入用户或项目 .npmrc）</h2>
<pre id="c3">registry=http://{{.Host}}/mirror/npm/
//{{.Host}}/:_auth=BASE64(user:pass)</pre>
<button class="copy" onclick="copyCmd('c3',this)">复制</button>
<p class="hint">BASE64(user:pass) 生成方式：<code>echo -n user:pass | base64</code>；
yarn berry 用 <code>.yarnrc.yml</code> 的 <code>npmRegistryServer</code> 与 <code>npmAuthIdent</code>，取值相同。</p>
</div>
<div class="cfg">
<h2>Node 版本管理器接入（选择所用工具的配置）</h2>
<pre id="c4">export NVM_NODEJS_ORG_MIRROR=http://user:pass@{{.Host}}/mirror/node   # nvm（macOS/Linux）
export N_NODE_MIRROR=http://user:pass@{{.Host}}/mirror/node           # n
export MISE_NODE_MIRROR_URL=http://user:pass@{{.Host}}/mirror/node    # mise
nvm node_mirror http://{{.Host}}/mirror/node/                         # nvm-windows（执行一次，写入 settings.txt）</pre>
<button class="copy" onclick="copyCmd('c4',this)">复制</button>
<p class="hint">配置后执行 <code>nvm install 22</code> / <code>n 22</code> / <code>mise use -g node@22</code>。
nvm-windows 与 mise 对 URL 内嵌凭据的支持未确认；返回 401 时检查认证配置，或登录安装包页手工下载；
fnm 同布局可用 <code>FNM_NODE_DIST_MIRROR</code>；Volta 不支持自定义镜像。</p>
</div>
<div class="cfg">
<h2>Go 接入（环境变量，把 user:pass 换成本服务账号）</h2>
<pre id="c5">export GOPROXY=http://user:pass@{{.Host}}/mirror/goproxy
export GOSUMDB=off
export GOTOOLCHAIN=local</pre>
<button class="copy" onclick="copyCmd('c5',this)">复制</button>
<p class="hint">GOPROXY 指定模块镜像；GOSUMDB=off 关闭校验数据库请求，项目 go.sum 中已有的哈希仍照常校验；GOTOOLCHAIN=local 使用本地工具链。缺模块时在外网执行 <code>pkgmirror download gomod -r go.sum</code> 后上传；需要其他 Go 版本时执行 <code>pkgmirror download golang &lt;版本&gt;</code> 下载后安装。</p>
</div>
<div class="cfg">
<h2>Go 版本管理器接入（选择所用工具的配置）</h2>
<pre id="c6">export MISE_GO_DOWNLOAD_MIRROR=http://user:pass@{{.Host}}/mirror/golang   # mise
export GO_DOWNLOAD_MIRROR=http://user:pass@{{.Host}}/mirror/golang        # asdf-golang</pre>
<button class="copy" onclick="copyCmd('c6',this)">复制</button>
<p class="hint">换源后照常 <code>mise use -g golang@1.23</code> / <code>asdf install golang 1.23.4</code>；
校验用的 .sha256 伴随文件随归档自动提供。gvm 与 <code>golang.org/dl</code> 辅助命令不支持自定义镜像，
走安装包页手工下载解压。</p>
</div>
</div>
` + copyScript + `
</body></html>`))

var browseTpl = template.Must(template.New("browse").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>包列表 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>PyPI 包列表</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<div class="cfgbar">pip：<code>pip install --index-url http://user:pass@{{.Host}}/mirror/simple 包名</code><br/>
uv：<code>export UV_INDEX_URL=http://user:pass@{{.Host}}/mirror/simple</code></div>
{{if .Rows}}<div class="filterbar"><input type="text" id="pkg-filter" placeholder="输入过滤包名，如 req" autocomplete="off"><span id="pkg-filter-hint" class="hint"></span></div>
<table><thead><tr><th>包名</th><th>文件数</th><th>总大小</th><th>操作</th></tr></thead><tbody>
{{range .Rows}}<tr><td><a href="/browse/pypi/{{.Name}}/">{{.Name}}</a></td><td>{{.Count}}</td><td>{{humanSize .Size}}</td><td><form method="post" action="/api/delete" class="delform"><input type="hidden" name="type" value="pypi"><input type="hidden" name="name" value="{{.Name}}"><input type="hidden" name="dryRun" value="1"><button type="submit" class="delbtn">删除</button></form></td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无包。请先执行 <code>pkgmirror download pypi</code> 下载并上传。</p>{{end}}
</div>` + filterScript + `
</body></html>`))

var browsePkgTpl = template.Must(template.New("browsePkg").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>{{.Name}} - PyPI 包 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>{{.Name}}</h1>
<table><thead><tr><th>文件</th><th>大小</th><th>sha256</th></tr></thead><tbody>
{{range .Files}}<tr><td><a href="/mirror/pypi/{{$.Name}}/{{.Name}}">{{.Name}}</a></td><td>{{humanSize .Size}}</td><td><code>{{.SHA256}}</code></td></tr>{{end}}
</tbody></table>
</div></body></html>`))

var pythonTpl = template.Must(template.New("python").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Python 安装包（python-build-standalone 构建）- pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>Python 安装包</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<p class="sub">安装包由 Astral 的 python-build-standalone 项目以 CPython 源码构建，供 <code>uv python install</code> 使用。归档可复制到同系统、同架构的机器，解压后配置 PATH。Windows 使用 <code>python -m pip</code>，建议预装 VC++ 运行库；Linux 需满足所选归档的 libc 要求。</p>
<div class="cfgbar">uv：<code>export UV_PYTHON_INSTALL_MIRROR=http://user:pass@{{.Host}}/mirror/python</code></div>
{{if .Rows}}<div class="filterbar"><input type="text" id="pkg-filter" placeholder="输入过滤，如 3.12 或 win" autocomplete="off"><span id="pkg-filter-hint" class="hint"></span></div>
<table><thead><tr><th>构建标签</th><th>Python 版本</th><th>文件</th><th>大小</th><th>操作</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{.Tag}}</td><td>{{.Version}}</td><td><a href="/mirror/python/{{.Tag}}/{{.Filename}}">{{.Filename}}</a></td><td>{{humanSize .Size}}</td><td><form method="post" action="/api/delete" class="delform"><input type="hidden" name="type" value="python"><input type="hidden" name="name" value="{{.Filename}}"><input type="hidden" name="dryRun" value="1"><button type="submit" class="delbtn">删除</button></form></td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无 Python 安装包。请先执行 <code>pkgmirror download python x.y</code> 下载并上传。</p>{{end}}
</div>` + filterScript + `
</body></html>`))

var python404Tpl = template.Must(template.New("python404").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>文件未同步 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>文件未同步</h1>
<p>请求的安装包 <code>{{.Path}}</code> 不在本镜像中。</p>
<p>请先执行 <code>pkgmirror download python &lt;版本&gt;</code> 下载对应文件，再用 <code>pkgmirror upload</code> 上传到本服务。</p>
</div></body></html>`))

var npmBrowseTpl = template.Must(template.New("npmBrowse").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>npm 包列表 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>npm 包列表</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<div class="cfgbar">npm：<code>npm config set registry http://{{.Host}}/mirror/npm/</code>（认证与 yarn 接入见首页）</div>
{{if .Rows}}<div class="filterbar"><input type="text" id="pkg-filter" placeholder="输入过滤包名，如 lodash" autocomplete="off"><span id="pkg-filter-hint" class="hint"></span></div>
<table><thead><tr><th>包名</th><th>文件数</th><th>总大小</th><th>操作</th></tr></thead><tbody>
{{range .Rows}}<tr><td><a href="/browse/npm/{{.Name}}/">{{.Name}}</a></td><td>{{.Count}}</td><td>{{humanSize .Size}}</td><td><form method="post" action="/api/delete" class="delform"><input type="hidden" name="type" value="npm"><input type="hidden" name="name" value="{{.Name}}"><input type="hidden" name="dryRun" value="1"><button type="submit" class="delbtn">删除</button></form></td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无 npm 包。请先执行 <code>pkgmirror download npm &lt;包名&gt;</code> 下载并上传。</p>{{end}}
</div>` + filterScript + `
</body></html>`))

var npmPkgTpl = template.Must(template.New("npmPkg").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>{{.Name}} - npm 包 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>{{.Name}}</h1>
<p class="sub">packument 是 npm 安装用的元数据文件，tarball 是包内容；两者共同构成镜像数据，均可点击下载。</p>
<table><thead><tr><th>文件</th><th>大小</th><th>sha256</th></tr></thead><tbody>
{{range .Files}}<tr><td><a href="/mirror/npm/{{$.Name}}/-/{{.Name}}">{{.Name}}</a></td><td>{{humanSize .Size}}</td><td><code>{{.SHA256}}</code></td></tr>{{end}}
</tbody></table>
<p class="hint">registry 协议入口（供 npm/pnpm/yarn）：<code>/mirror/npm/{{.Name}}</code></p>
</div></body></html>`))

var nodeTpl = template.Must(template.New("node").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Node.js 安装包 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>Node.js 安装包</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<p class="sub">官方 nodejs.org/dist 的可移植归档（Linux/macOS 为 tar.xz，Windows 为 zip），解压后把 bin 目录加入 PATH 即可运行。</p>
<div class="cfgbar">nvm：<code>export NVM_NODEJS_ORG_MIRROR=http://user:pass@{{.Host}}/mirror/node</code><br/>
n：<code>export N_NODE_MIRROR=http://user:pass@{{.Host}}/mirror/node</code><br/>
mise：<code>export MISE_NODE_MIRROR_URL=http://user:pass@{{.Host}}/mirror/node</code><br/>
nvm-windows：<code>nvm node_mirror http://{{.Host}}/mirror/node/</code>（执行一次，写入 settings.txt）<br/>
四选一（用哪个工具执行对应一条）；nvm-windows / mise 的凭据限制与 fnm、Volta 情况见首页。</div>
{{if .Rows}}<div class="filterbar"><input type="text" id="pkg-filter" placeholder="输入过滤，如 22 或 win" autocomplete="off"><span id="pkg-filter-hint" class="hint"></span></div>
<table><thead><tr><th>版本</th><th>文件</th><th>大小</th><th>操作</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{.Version}}</td><td><a href="/mirror/node/{{.Version}}/{{.Filename}}">{{.Filename}}</a></td><td>{{humanSize .Size}}</td><td>{{if ne .Filename "SHASUMS256.txt"}}<form method="post" action="/api/delete" class="delform"><input type="hidden" name="type" value="node"><input type="hidden" name="name" value="{{.Filename}}"><input type="hidden" name="dryRun" value="1"><button type="submit" class="delbtn">删除</button></form>{{end}}</td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无 Node 安装包。请先执行 <code>pkgmirror download node &lt;版本&gt;</code> 下载并上传。</p>{{end}}
</div>` + filterScript + `
</body></html>`))

var node404Tpl = template.Must(template.New("node404").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>文件未同步 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>文件未同步</h1>
<p>请求的 Node 发行文件 <code>{{.Path}}</code> 不在本镜像中。</p>
<p>请先执行 <code>pkgmirror download node &lt;版本&gt;</code> 下载对应文件，再用 <code>pkgmirror upload</code> 上传到本服务。</p>
</div></body></html>`))

var npm404Tpl = template.Must(template.New("npm404").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>文件未同步 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>文件未同步</h1>
<p>请求的 npm 资源 <code>{{.Path}}</code> 不在本镜像中。</p>
<p>请先在外网执行 <code>pkgmirror download npm &lt;包名&gt;</code>（或补充 <code>-r package-lock.json</code>）下载对应文件，再用 <code>pkgmirror upload</code> 上传到本服务。</p>
</div></body></html>`))

var golangTpl = template.Must(template.New("golang").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Go 安装包 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>Go 安装包</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<p class="sub">官方 dl.google.com/go 的发行归档（unix 为 tar.gz，windows 为 zip），解压后把其中的 go 目录配进 PATH 即可运行。布局与官方一致，全部文件平铺。</p>
<div class="cfgbar">mise：<code>export MISE_GO_DOWNLOAD_MIRROR=http://user:pass@{{.Host}}/mirror/golang</code><br/>
asdf-golang：<code>export GO_DOWNLOAD_MIRROR=http://user:pass@{{.Host}}/mirror/golang</code><br/>
gvm 与 golang.org/dl 辅助命令不支持自定义镜像，从下表手工下载解压。</div>
{{if .Rows}}<div class="filterbar"><input type="text" id="pkg-filter" placeholder="输入过滤，如 1.23 或 linux" autocomplete="off"><span id="pkg-filter-hint" class="hint"></span></div>
<table><thead><tr><th>版本</th><th>文件</th><th>大小</th><th>操作</th></tr></thead><tbody>
{{range .Rows}}<tr><td>{{.Version}}</td><td><a href="/mirror/golang/{{.Filename}}">{{.Filename}}</a></td><td>{{humanSize .Size}}</td><td><form method="post" action="/api/delete" class="delform"><input type="hidden" name="type" value="golang"><input type="hidden" name="name" value="{{.Filename}}"><input type="hidden" name="dryRun" value="1"><button type="submit" class="delbtn">删除</button></form></td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无 Go 安装包。请先执行 <code>pkgmirror download golang &lt;版本&gt;</code> 下载并上传。</p>{{end}}
</div>` + filterScript + `
</body></html>`))

var golang404Tpl = template.Must(template.New("golang404").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>文件未同步 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>文件未同步</h1>
<p>请求的 Go 发行文件 <code>{{.Path}}</code> 不在本镜像中。</p>
<p>请先执行 <code>pkgmirror download golang &lt;版本&gt;</code> 下载对应文件，再用 <code>pkgmirror upload</code> 上传到本服务。</p>
</div></body></html>`))

var gomodBrowseTpl = template.Must(template.New("gomodBrowse").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Go 模块列表 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>Go 模块列表</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<div class="cfgbar">Go 命令：<code>export GOPROXY=http://user:pass@{{.Host}}/mirror/goproxy GOSUMDB=off GOTOOLCHAIN=local</code>（完整接入说明见首页）</div>
{{if .Rows}}<div class="filterbar"><input type="text" id="pkg-filter" placeholder="输入过滤模块路径，如 cobra" autocomplete="off"><span id="pkg-filter-hint" class="hint"></span></div>
<table><thead><tr><th>模块路径</th><th>文件数</th><th>总大小</th><th>操作</th></tr></thead><tbody>
{{range .Rows}}<tr><td><a href="/browse/gomod/{{.Name}}/">{{.Name}}</a></td><td>{{.Count}}</td><td>{{humanSize .Size}}</td><td><form method="post" action="/api/delete" class="delform"><input type="hidden" name="type" value="gomod"><input type="hidden" name="name" value="{{.Name}}"><input type="hidden" name="dryRun" value="1"><button type="submit" class="delbtn">删除</button></form></td></tr>{{end}}
</tbody></table>{{else}}<p class="hint">暂无 Go 模块。请先执行 <code>pkgmirror download gomod -r go.sum</code> 下载并上传。</p>{{end}}
</div>` + filterScript + `
</body></html>`))

var gomodPkgTpl = template.Must(template.New("gomodPkg").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>{{.Name}} - Go 模块 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>{{.Name}}</h1>
<p class="sub">每个版本三件套：.mod（依赖声明）、.zip（模块源码）、.info（版本元数据）；均为 module proxy 协议数据，原样供内网 go 命令拉取。</p>
<table><thead><tr><th>文件</th><th>大小</th></tr></thead><tbody>
{{range .Files}}<tr><td><a href="/mirror/goproxy/{{$.Escaped}}/@v/{{.Name}}">{{.Name}}</a></td><td>{{humanSize .Size}}</td></tr>{{end}}
</tbody></table>
<p class="hint">module proxy 协议入口：<code>/mirror/goproxy/{{.Escaped}}/@v/list</code>（版本列表）与 <code>/mirror/goproxy/{{.Escaped}}/@latest</code>。</p>
</div></body></html>`))

var gomod404Tpl = template.Must(template.New("gomod404").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>资源未同步 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>资源未同步</h1>
<p>请求的 Go 模块资源 <code>{{.Path}}</code> 不在本镜像中。</p>
<p>请先在外网执行 <code>pkgmirror download gomod -r go.sum</code>（或命令行 <code>模块@版本</code> 补单个）下载对应文件，再用 <code>pkgmirror upload</code> 上传到本服务。</p>
</div></body></html>`))

// deletePreviewTpl 展示目标与级联清单，各区块通过独立表单执行删除。
// 级联表单携带 deps=1；没有级联内容时隐藏该区块。取消按钮返回类型列表。
var deletePreviewTpl = template.Must(template.New("deletePreview").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>删除预览 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>删除预览 <a class="cancelbtn" href="{{.Back}}">取消</a></h1>
<p class="sub">{{.Type}} {{.Name}}</p>
<form method="post" action="/api/delete">
<input type="hidden" name="type" value="{{.Type}}">
<input type="hidden" name="name" value="{{.Name}}">
<h2>删除目标 <button type="submit" class="delmain">{{.BtnSelf}}</button></h2>
<table><thead><tr><th>名称</th><th>文件数</th><th>大小</th></tr></thead><tbody>
{{range .Targets}}<tr><td>{{.Name}}</td><td>{{len .Files}}</td><td>{{humanSize .Size}}</td></tr>{{end}}
</tbody></table>
</form>
{{if .Cascades}}<form method="post" action="/api/delete">
<input type="hidden" name="type" value="{{.Type}}">
<input type="hidden" name="name" value="{{.Name}}">
<input type="hidden" name="deps" value="1">
<h2>{{.CascadeTitle}} <button type="submit" class="delmain">{{.BtnFull}}</button></h2>
<table><thead><tr><th>名称</th><th>文件数</th><th>大小</th></tr></thead><tbody>
{{range .Cascades}}<tr><td>{{.Name}}</td><td>{{len .Files}}</td><td>{{humanSize .Size}}</td></tr>{{end}}
</tbody></table>
<p class="hint">级联部分：{{.CascadeCount}} 个文件，{{humanSize .CascadeSize}}；目标与级联合计：{{.TotalCount}} 个文件，{{humanSize .TotalSize}}</p>
</form>
{{else}}<p class="hint">没有可级联删除的内容。</p>{{end}}
</div></body></html>`))

var uploadTpl = template.Must(template.New("upload").Funcs(tmplFuncs).Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>上传 - pkgmirror</title>` + pageCSS + `</head><body>
` + navHead + `
<h1>上传文件</h1>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
<form method="post" action="/api/upload" enctype="multipart/form-data">
<label for="f">文件（wheel / sdist / Python 安装包 / npm tgz / Node 归档 / Go 归档 / Go 模块文件）</label>
<input type="file" name="file" id="f" required>
<label for="t">类型</label>
<select name="type" id="t"><option value="pypi">pypi 包（wheel/sdist）</option><option value="python">python 安装包</option><option value="npm">npm 包（tgz / packument.json）</option><option value="node">node 安装包（tar.xz / zip / SHASUMS256.txt）</option><option value="golang">golang 安装包（tar.gz / zip）</option><option value="gomod">gomod 模块文件（.mod / .zip / .info）</option></select>
<label for="n">包名 / 模块路径（pypi 留空自动解析；npm、gomod 必填）</label>
<input type="text" name="name" id="n" placeholder="pypi 可留空；npm 含 @scope/ 前缀；gomod 填模块路径">
<label for="g">构建标签（python）或版本号（node）</label>
<input type="text" name="tag" id="g" placeholder="python 填 8 位日期（如 20261021）；node 填版本（如 v22.14.0）">
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
