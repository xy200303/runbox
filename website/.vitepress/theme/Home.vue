<script setup lang="ts">
import { withBase } from 'vitepress'
import CodeTabs from './components/CodeTabs.vue'
import LineChart from './components/LineChart.vue'

const stats = [
  { value: '1 个容器', label: '承载全部身份', note: '不为每个用户新建运行时' },
  { value: '1 个卷', label: '工作区与状态', note: 'workspaces/ 与 SQLite 同卷' },
  { value: '~170 MB', label: 'minimal 镜像下载', note: 'full 约 600 MB，toolbox 约 285 MB' },
  { value: '0', label: '外部数据库与编排器', note: 'SQLite 内嵌在 runner 里' }
]

const features = [
  {
    t: '单容器，多身份',
    d: 'HTTP runner 与执行环境共处一个长期运行的容器。每个身份用自己的 UID；同一身份串行，不同身份并发。'
  },
  {
    t: '按身份的文件隔离',
    d: '私有工作区、工作区配额和符号链接安全的路径解析。别的身份访问同一个相对路径，看到的是它自己的目录，或者 404。'
  },
  {
    t: '持久安装环境',
    d: 'HOME、XDG、pip 与 npm 前缀都在工作区里。装过的包、编译出来的中间文件跨调用保留，并计入配额。'
  },
  {
    t: '路径就是接口',
    d: '上传返回工作区路径，执行产物按相对路径取回，目录打成一个 tar 下载。没有 file_id 需要翻译，Agent 直接照路径用。'
  },
  {
    t: '有界执行',
    d: '命令长度、并发、超时、stdout/stderr 大小、产物数量与大小、工作区配额、保留期都有上界，不靠调用方自觉。'
  },
  {
    t: 'fail-closed 与可观测',
    d: '只读根文件系统、能力最小集、no-new-privileges，缺能力直接拒绝启动；/ready、/metrics 和结构化日志都在。'
  }
]

const proof = [
  { value: '140', label: '攻击断言', note: '常规 78 · 深度 36 · 混淆 26' },
  { value: '0', label: '越权 / 穿越成功', note: '无跨身份读写，无逃逸' },
  { value: '8.5 ms', label: 'exec p50', note: 'p95 30.9 ms，max 33 ms' },
  { value: '667 exec/s', label: '16 路并发', note: '超出并发上限立即 409' }
]

const throughput = [
  { label: '2 路', value: 67 },
  { label: '4 路', value: 367 },
  { label: '8 路', value: 493 },
  { label: '16 路', value: 667 }
]

const quickstart = [
  {
    id: 'docker',
    label: 'Docker',
    code: `docker run -d --name runbox \\
  --read-only \\
  --tmpfs /tmp:rw,exec,nosuid,size=512m \\
  --cap-drop ALL \\
  --cap-add CHOWN --cap-add FOWNER --cap-add SETUID \\
  --cap-add SETGID --cap-add DAC_OVERRIDE --cap-add KILL \\
  --security-opt no-new-privileges:true \\
  -p 8099:8099 \\
  -e RUNBOX_API_KEY=$(openssl rand -hex 32) \\
  -v runbox-data:/workspace \\
  ghcr.io/xy200303/runbox:latest`,
    note: '换 :latest-minimal 跑纯代码与文本任务，换 :latest-toolbox 让 pip/npm 能编译原生扩展。'
  },
  {
    id: 'curl',
    label: 'cURL',
    code: `curl -fsS http://localhost:8099/v1/exec \\
  -H "Authorization: Bearer $RUNBOX_API_KEY" \\
  -H "X-Runbox-Namespace: team-a" \\
  -H "X-Runbox-Principal: user-42" \\
  -H "Content-Type: application/json" \\
  -d '{"command":"mkdir -p out && echo hello > out/report.txt"}'

# {"exit_code":0,"files":["out/report.txt"],"duration_ms":4}

curl -fsS "http://localhost:8099/v1/path?path=out/report.txt" \\
  -H "Authorization: Bearer $RUNBOX_API_KEY" \\
  -H "X-Runbox-Namespace: team-a" \\
  -H "X-Runbox-Principal: user-42"`,
    note: '身份只认一种写法：X-Runbox-Identity，或者 X-Runbox-Namespace 与 X-Runbox-Principal 成对出现。'
  },
  {
    id: 'python',
    label: 'Python',
    code: `pip install runbox-sdk

from runbox import Client, Identity

client = Client(
    "http://localhost:8099",
    api_key=os.environ["RUNBOX_API_KEY"],
    identity=Identity(namespace="team-a", principal="user-42"),
)

result = client.exec('python3 -c "print(6 * 7)"')
print(result["stdout"])          # 42
client.download_path("out/report.csv", "report.csv")
client.download_archive(["out", "tmp"], "artifacts.tar")`,
    note: '纯标准库实现；上传从磁盘流式读取，下载直接落盘。'
  },
  {
    id: 'node',
    label: 'Node.js',
    code: `npm install runbox-sdk

import { Client } from 'runbox-sdk'

const client = new Client({
  baseUrl: 'http://localhost:8099',
  apiKey: process.env.RUNBOX_API_KEY,
  identity: { namespace: 'team-a', principal: 'user-42' },
})

const result = await client.exec('echo hello')
await client.downloadArchive(['out'], 'artifacts.tar')`,
    note: '带 TypeScript 类型；FormData 上传、tar 打包与路径下载都在 SDK 里。'
  },
  {
    id: 'go',
    label: 'Go',
    code: `go get github.com/xy200303/runbox/sdk/go

client, _ := sandbox.NewClient(sandbox.ClientOptions{
    BaseURL:  "http://localhost:8099",
    APIKey:   os.Getenv("RUNBOX_API_KEY"),
    Identity: sandbox.ScopedIdentity("team-a", "user-42"),
})

result, _ := client.Run(ctx, sandbox.ExecRequest{
    Command: "python3 analyze.py files/input.csv",
    Files:   []sandbox.FileUpload{{Name: "input.csv", Reader: f}},
})`,
    note: 'Client 同时实现 sandbox.Backend；附件从 io.Reader 流式发送。'
  }
]

const sdks = [
  {
    lang: 'Go',
    install: 'go get github.com/xy200303/runbox/sdk/go',
    desc: '标准库 HTTP 客户端，流式附件，实现 Backend 接口。',
    link: 'https://github.com/xy200303/runbox/tree/main/sdk/go'
  },
  {
    lang: 'Python',
    install: 'pip install runbox-sdk',
    desc: '仅标准库；流式上传、流式落盘、tar 批量下载。',
    link: 'https://github.com/xy200303/runbox/tree/main/sdk/python'
  },
  {
    lang: 'Node.js',
    install: 'npm install runbox-sdk',
    desc: 'TypeScript 类型，ESM，FormData 与路径下载。',
    link: 'https://github.com/xy200303/runbox/tree/main/sdk/node'
  }
]

const images = [
  { tag: 'latest-minimal', size: '约 170 MB', use: '代码、文本、PDF 抽取' },
  { tag: 'latest', size: '约 600 MB', use: 'Office 文档、表格、pandas/numpy、原生扩展编译' },
  { tag: 'latest-toolbox', size: '约 285 MB', use: '只编译，不需要 Office' }
]

const faqs = [
  {
    q: '每个用户都会新建容器吗？',
    a: '不会。所有身份共享一个长期运行的容器，每个身份在里面拥有一个 UID 和私有工作区。这正是它能单机部署的原因。'
  },
  {
    q: '中间文件怎么下载？必须为每个文件生成 ID 吗？',
    a: '不需要。任意文件都能按相对路径下载，例如 GET /v1/path?path=tmp/step2/data.csv；目录用 GET /v1/archive?path=out 打成 tar。上传、执行产物、下载都只用路径。'
  },
  {
    q: '可以执行 apt-get 吗？',
    a: '不能。身份不是 root，根文件系统只读。系统级依赖应该在镜像构建时预装；pip/npm 安装落在各自工作区，不污染别人。'
  },
  {
    q: '可以执行不受信任的代码吗？',
    a: '不能把它当作强多租户边界。Runbox 用容器 namespace/cgroup 加按 UID 的文件权限，内核是共享的。面对互相敌对的租户，请放到 gVisor 或 microVM 下运行。'
  },
  {
    q: '和 dify-sandbox 有什么区别？',
    a: '形态相近：一个容器，每次执行切 UID。区别是 Runbox 的身份与 UID 持久化，工作区跨调用保留，文件可以按路径或 tar 打包下载，面向独立应用而不是某个平台内部的代码节点。'
  },
  {
    q: '数据怎么备份？',
    a: '备份 workspaces/（如果中间状态重要）和 state/runbox.db。SQLite 跑在 WAL 模式，建议停容器或用 SQLite 的备份接口复制，别在运行中直接拷主文件。'
  }
]
</script>

<template>
  <div class="landing">
    <!-- ============ Hero ============ -->
    <section class="hero">
      <div class="hero-inner">
        <div class="hero-copy">
          <a
            class="badge reveal"
            href="https://github.com/xy200303/runbox/releases/latest"
            target="_blank"
            rel="noreferrer"
          >
            <span class="badge-ver">v0.2.3</span>
            <span class="badge-sep" />
            <span class="badge-text">MIT · 单机服务器 · 一个容器</span>
          </a>

          <h1 class="hero-title reveal">
            Run in a box<span class="dot">.</span>
          </h1>

          <p class="hero-lead reveal">
            专为单机服务器打造的轻量 Agent 沙箱。
          </p>

          <p class="hero-body reveal">
            给 Agent 一个可持久化的 Linux 工作区：上传输入、执行 Python / Node / Shell、按路径取回产物。
            多个身份共享一个容器，各自拥有 UID、私有目录、配额和安装环境，不需要编排器，也不需要外部数据库。
          </p>

          <div class="actions reveal">
            <a class="btn primary" :href="withBase('/guide/quickstart')">快速开始</a>
            <a class="btn ghost" href="https://github.com/xy200303/runbox" target="_blank" rel="noreferrer">
              查看源码
            </a>
          </div>

          <ul class="hero-facts reveal">
            <li><b>镜像</b>Debian 12 slim 基座</li>
            <li><b>协议</b>HTTP + Bearer</li>
            <li><b>语言</b>Go，单二进制</li>
          </ul>
        </div>

        <div class="hero-panel reveal">
          <div class="terminal">
            <div class="terminal-bar">
              <span class="lights"><i /><i /><i /></span>
              <span class="terminal-title">runbox — 一个容器，两个身份</span>
            </div>
            <pre class="terminal-body"><code><span class="p">$</span> docker run -d --name runbox \
    -p 8099:8099 \
    -e RUNBOX_API_KEY=$RUNBOX_API_KEY \
    -v runbox-data:/workspace \
    ghcr.io/xy200303/runbox:latest

<span class="p">$</span> curl -fsS localhost:8099/v1/exec \
    -H <span class="s">"Authorization: Bearer $RUNBOX_API_KEY"</span> \
    -H <span class="s">"X-Runbox-Identity: alice"</span> \
    -d <span class="s">'{"command":"echo hello > out/report.txt"}'</span>

<span class="j">{"exit_code":0,"files":["out/report.txt"]}</span>

<span class="c"># 同一个相对路径，各自身份看到各自的工作区</span>
<span class="p">$</span> tree -L 2 /workspace/workspaces/
/workspace/workspaces/
├── 3f9c…alice
│   ├── files/  out/  .local/
└── 71ab…bob
    ├── files/  out/  .local/</code></pre>
          </div>
          <p class="hero-panel-note">一个长期运行的进程，按身份分配 UID 20000+n。</p>
        </div>
      </div>

      <ul class="stats">
        <li v-for="(s, i) in stats" :key="s.label" v-reveal="i * 60">
          <strong class="tnum">{{ s.value }}</strong>
          <span class="stat-label">{{ s.label }}</span>
          <span class="stat-note">{{ s.note }}</span>
        </li>
      </ul>
    </section>

    <!-- ============ 为什么 ============ -->
    <section class="section">
      <div class="inner">
        <header class="head">
          <span class="eyebrow" v-reveal><i>01</i>为什么是 Runbox</span>
          <h2 class="display" v-reveal>不是每个用户一个容器，<br />是每个容器很多身份。</h2>
          <p class="lead" v-reveal>
            container-per-sandbox 和 microVM 都为每个沙箱准备一套独立运行时。面对互相敌对的租户，这是正确的取舍；
            但在单机上意味着每个用户多一个容器或虚拟机，往往还多一份 Python 和 LibreOffice。
          </p>
        </header>

        <div class="table-wrap" v-reveal>
          <table class="compare">
            <thead>
              <tr>
                <th></th>
                <th>每个沙箱一个容器</th>
                <th>每个沙箱一台 microVM</th>
                <th class="accent-col">Runbox</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <td>隔离单位</td>
                <td>容器</td>
                <td>虚拟机</td>
                <td class="accent-col">单容器内 UID + 私有工作区</td>
              </tr>
              <tr>
                <td>每个身份新建运行时</td>
                <td>是</td>
                <td>是</td>
                <td class="accent-col">否</td>
              </tr>
              <tr>
                <td>单机部署</td>
                <td>控制面 + 多个容器</td>
                <td>KVM + 运行时 + 镜像</td>
                <td class="accent-col">一个二进制、一个容器、一个卷</td>
              </tr>
              <tr>
                <td>文件隔离</td>
                <td>依赖卷或 API</td>
                <td>强</td>
                <td class="accent-col">内置：工作区、配额、路径解析</td>
              </tr>
              <tr>
                <td>适合</td>
                <td>多机、强隔离</td>
                <td>敌对租户</td>
                <td class="accent-col">内部或半可信、单机</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="callout" v-reveal>
          共享内核不是 microVM 边界。面对互相敌对的租户，请把 Runbox 放到 gVisor 或 microVM 下运行。
        </p>
      </div>
    </section>

    <!-- ============ 能力 ============ -->
    <section class="section alt">
      <div class="inner">
        <header class="head">
          <span class="eyebrow" v-reveal><i>02</i>能力</span>
          <h2 class="display" v-reveal>轻量，但文件、配额和执行边界都是真的。</h2>
          <p class="lead" v-reveal>
            省掉的是编排器、外部数据库和每用户运行时，不是隔离。下面每一条都能在接口上验证。
          </p>
        </header>

        <div class="grid features">
          <article v-for="(f, i) in features" :key="f.t" v-reveal="(i % 3) * 70" class="card">
            <span class="card-no tnum">{{ String(i + 1).padStart(2, '0') }}</span>
            <h3>{{ f.t }}</h3>
            <p>{{ f.d }}</p>
          </article>
        </div>
      </div>
    </section>

    <!-- ============ 工作机制 ============ -->
    <section class="section">
      <div class="inner">
        <header class="head">
          <span class="eyebrow" v-reveal><i>03</i>工作机制</span>
          <h2 class="display" v-reveal>路径是相对的，边界由内核判定。</h2>
          <p class="lead" v-reveal>
            每个请求带一个身份。runner 把命令的工作目录指向该身份的目录，在 exec 之前切换 UID。
            命令写 <code>out/report.csv</code> 时，落点由 cwd、HOME 和目录权限共同决定。
          </p>
        </header>

        <div class="flow" v-reveal>
          <div class="node">
            <span class="node-label">调用方</span>
            <strong>Agent / 应用</strong>
            <small>Bearer + 身份头</small>
          </div>
          <span class="arrow" aria-hidden="true">→</span>
          <div class="node key">
            <span class="node-label">HTTP</span>
            <strong>runboxd</strong>
            <small>Gin · 身份校验 · 路径解析</small>
          </div>
          <span class="arrow" aria-hidden="true">→</span>
          <div class="node">
            <span class="node-label">执行</span>
            <strong>/bin/sh</strong>
            <small>UID 20000+n · 独立进程组</small>
          </div>
        </div>

        <pre class="tree" v-reveal><code>RUNBOX_ROOT/
├── workspaces/&lt;identity-key&gt;/       0750，属主为该身份的 UID
│   ├── files/                       上传的输入
│   ├── out/                         执行产物
│   ├── .local/  .npm-global/        持久的 pip / npm 安装
│   ├── .cache/  .config/  .tmp/     XDG 状态与临时文件
│   └── ...
└── state/
    ├── runbox.db                    SQLite：身份、UID、元数据
    ├── runbox.db-wal
    └── runbox.db-shm</code></pre>
      </div>
    </section>

    <!-- ============ 快速开始 ============ -->
    <section class="section alt">
      <div class="inner">
        <header class="head">
          <span class="eyebrow" v-reveal><i>04</i>快速开始</span>
          <h2 class="display" v-reveal>一个容器起来，剩下的交给 HTTP。</h2>
          <p class="lead" v-reveal>
            三种语言 SDK 与 cURL 走同一套契约：Bearer 认证、二选一的身份头、按路径传文件。
          </p>
        </header>

        <div v-reveal>
          <CodeTabs :items="quickstart" label="快速开始示例" />
        </div>
      </div>
    </section>

    <!-- ============ SDK ============ -->
    <section class="section">
      <div class="inner">
        <header class="head">
          <span class="eyebrow" v-reveal><i>05</i>SDK</span>
          <h2 class="display" v-reveal>按语言取用，接口保持同一套。</h2>
        </header>

        <div class="grid sdks">
          <a
            v-for="(s, i) in sdks"
            :key="s.lang"
            v-reveal="i * 60"
            class="sdk"
            :href="s.link"
            target="_blank"
            rel="noreferrer"
          >
            <span class="sdk-lang">{{ s.lang }}</span>
            <code>{{ s.install }}</code>
            <p>{{ s.desc }}</p>
          </a>
        </div>
      </div>
    </section>

    <!-- ============ 部署 ============ -->
    <section class="section alt">
      <div class="inner">
        <header class="head">
          <span class="eyebrow" v-reveal><i>06</i>部署</span>
          <h2 class="display" v-reveal>单机优先，也允许把边界交给 gVisor。</h2>
        </header>

        <div class="grid two">
          <article class="panel" v-reveal>
            <h3>一个卷搞定持久化</h3>
            <p>
              把数据根目录挂载成卷就够：<code>workspaces/</code> 和 <code>state/runbox.db</code> 都在里面。
              备份、迁移、换机器搬卷即可。
            </p>
            <pre><code>docker run -d --name runbox \
  -p 8099:8099 \
  -e RUNBOX_API_KEY=$RUNBOX_API_KEY \
  -v runbox-data:/workspace \
  ghcr.io/xy200303/runbox:latest</code></pre>
          </article>

          <article class="panel" v-reveal="60">
            <h3>默认安全姿态</h3>
            <p>
              只读根、丢弃全部能力后只补回所有权相关项、no-new-privileges、资源上限。
              启动时缺少必需能力会直接失败，不会带着残缺权限继续跑。
            </p>
            <pre><code>cap_drop: [ALL]
cap_add: [CHOWN, FOWNER, SETUID,
          SETGID, DAC_OVERRIDE, KILL]
security_opt:
  - no-new-privileges:true
read_only: true</code></pre>
          </article>
        </div>

        <div class="table-wrap images" v-reveal>
          <table class="compare">
            <thead>
              <tr>
                <th>镜像</th>
                <th>下载体积</th>
                <th>适合</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="img in images" :key="img.tag">
                <td><code>ghcr.io/xy200303/runbox:{{ img.tag }}</code></td>
                <td class="tnum">{{ img.size }}</td>
                <td>{{ img.use }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="callout" v-reveal>
          egress 过滤、文件系统配额与多副本路由属于部署决策，做法见 <a :href="withBase('/guide/deploy')">部署与加固</a>。
        </p>
      </div>
    </section>

    <!-- ============ FAQ ============ -->
    <section class="section">
      <div class="inner narrow">
        <header class="head">
          <span class="eyebrow" v-reveal><i>07</i>FAQ</span>
          <h2 class="display" v-reveal>接入前最常问的几个问题。</h2>
        </header>

        <div class="faq-list">
          <details v-for="(f, i) in faqs" :key="f.q" v-reveal="(i % 3) * 50" class="faq-item" :open="i === 0">
            <summary>{{ f.q }}</summary>
            <p>{{ f.a }}</p>
          </details>
        </div>
      </div>
    </section>

    <!-- ============ 验证 ============ -->
    <section class="section alt">
      <div class="inner">
        <header class="head">
          <span class="eyebrow" v-reveal><i>08</i>验证</span>
          <h2 class="display" v-reveal>攻击面用数字说话。</h2>
          <p class="lead" v-reveal>
            140 项攻击断言：认证、身份头、路径穿越、容器边界，加上深度隔离、命令混淆与脚本套娃。
            每一套都配了可重跑的脚本，结果和限制一起写进报告。
          </p>
        </header>

        <ul class="proof">
          <li v-for="(item, i) in proof" :key="item.label" v-reveal="i * 60">
            <strong class="tnum">{{ item.value }}</strong>
            <span class="stat-label">{{ item.label }}</span>
            <span class="stat-note">{{ item.note }}</span>
          </li>
        </ul>

        <div class="proof-chart" v-reveal>
          <LineChart
            :points="throughput"
            unit=" exec/s"
            :height="210"
            :labels="false"
            note="不同身份并行执行 true：2 路 67、4 路 367、8 路 493、16 路 667 exec/s。单次执行 p50 8.5 ms。"
          />
        </div>

        <p class="more" v-reveal>
          <a :href="withBase('/validation')">查看完整验证报告</a>
        </p>
      </div>
    </section>

    <!-- ============ 收尾 ============ -->
    <section class="final">
      <div class="final-inner">
        <span class="eyebrow light" v-reveal>Run in a box.</span>
        <h2 class="display light" v-reveal>一台服务器，一个盒子。</h2>
        <p class="final-sub" v-reveal>
          从 Docker 或 SDK 开始，十分钟内跑通第一条命令。MIT 许可，源码在 GitHub。
        </p>
        <div class="actions center" v-reveal>
          <a class="btn primary on-dark" :href="withBase('/guide/quickstart')">快速开始</a>
          <a class="btn ghost on-dark" href="https://github.com/xy200303/runbox" target="_blank" rel="noreferrer">
            GitHub
          </a>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.landing {
  color: var(--rb-ink);
}

/* ---------- Hero ---------- */
.hero {
  border-bottom: 1px solid var(--rb-line-soft);
  background: linear-gradient(180deg, #fbfbfa 0%, #ffffff 62%);
}
.hero-inner {
  max-width: var(--rb-container);
  margin: 0 auto;
  padding: 76px 24px 0;
  display: grid;
  grid-template-columns: minmax(0, 1.05fr) minmax(0, 0.95fr);
  gap: 56px;
  align-items: start;
}
/* grid 子项默认 min-width:auto，宽 pre 会把整列撑出容器 */
.hero-inner > * {
  min-width: 0;
}

.badge {
  display: inline-flex;
  align-items: center;
  gap: 9px;
  padding: 5px 12px 5px 8px;
  border: 1px solid var(--rb-line);
  border-radius: 999px;
  font-size: 12.5px;
  color: var(--rb-ink-2);
  background: #fff;
  text-decoration: none;
}
.badge:hover {
  border-color: var(--rb-line-strong);
  color: var(--rb-ink);
}
.badge-ver {
  padding: 2px 7px;
  border-radius: 999px;
  background: var(--rb-emerald-50);
  color: var(--rb-emerald-600);
  font-family: var(--rb-font-mono);
  font-size: 11.5px;
  font-weight: 600;
}
.badge-sep {
  width: 1px;
  height: 11px;
  background: var(--rb-line);
}

.hero-title {
  margin: 22px 0 0;
  font-size: clamp(46px, 6.4vw, 76px);
  line-height: 1.02;
  letter-spacing: -0.045em;
  font-weight: 600;
}
.hero-title .dot {
  color: var(--rb-emerald-600);
}
.hero-lead {
  margin: 18px 0 0;
  font-size: clamp(17px, 1.6vw, 20px);
  line-height: 1.6;
  color: var(--rb-ink);
  letter-spacing: -0.005em;
}
.hero-body {
  margin: 14px 0 0;
  max-width: 34em;
  font-size: 15.5px;
  line-height: 1.8;
  color: var(--rb-ink-2);
}

.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 28px;
}
.actions.center {
  justify-content: center;
}
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 40px;
  padding: 0 18px;
  border-radius: var(--rb-radius-sm);
  border: 1px solid transparent;
  font-size: 14px;
  font-weight: 500;
  text-decoration: none;
  transition: transform 0.18s cubic-bezier(0.16, 1, 0.3, 1), background 0.18s,
    border-color 0.18s, color 0.18s;
}
.btn:active {
  transform: translateY(1px);
}
.btn.primary {
  background: var(--rb-ink);
  color: #fff;
}
.btn.primary:hover {
  background: #000;
}
.btn.ghost {
  background: #fff;
  border-color: var(--rb-line);
  color: var(--rb-ink);
}
.btn.ghost:hover {
  border-color: var(--rb-line-strong);
}

.hero-facts {
  display: flex;
  flex-wrap: wrap;
  gap: 26px;
  margin: 34px 0 0;
  padding: 0;
  list-style: none;
}
.hero-facts li {
  font-size: 12.5px;
  color: var(--rb-ink-3);
}
.hero-facts b {
  display: block;
  margin-bottom: 3px;
  font-weight: 500;
  color: var(--rb-ink);
  font-size: 13px;
}

/* ---------- 终端 ---------- */
.hero-panel {
  position: relative;
}
.terminal {
  border-radius: var(--rb-radius-lg);
  background: var(--rb-dark);
  box-shadow: var(--rb-shadow), 0 40px 80px -60px rgba(13, 17, 23, 0.6);
  overflow: hidden;
}
.terminal-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 14px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.07);
}
.lights {
  display: flex;
  gap: 5px;
}
.lights i {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.16);
}
.terminal-title {
  font-size: 11.5px;
  color: #7d8894;
  font-family: var(--rb-font-mono);
}
.terminal-body {
  margin: 0;
  padding: 18px 20px 22px;
  overflow-x: auto;
  font-family: var(--rb-font-mono);
  font-size: 12.5px;
  line-height: 1.78;
  color: #b7c2cd;
  tab-size: 2;
}
.terminal-body code {
  font: inherit;
  white-space: pre;
}
.terminal-body .p {
  color: var(--rb-emerald-bright);
}
.terminal-body .s {
  color: #e6edf3;
}
.terminal-body .j {
  color: #8fd3b6;
}
.terminal-body .c {
  color: #5f6b78;
}
.hero-panel-note {
  margin: 12px 2px 0;
  font-size: 12.5px;
  color: var(--rb-ink-3);
}

/* ---------- 数字条 ---------- */
.proof {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  margin: 34px 0 0;
  padding: 0;
  list-style: none;
  border-top: 1px solid var(--rb-line);
  border-bottom: 1px solid var(--rb-line);
}
.proof li {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 20px 18px 22px 0;
  border-right: 1px solid var(--rb-line-soft);
}
.proof li:last-child {
  border-right: 0;
}
.proof strong {
  font-size: 21px;
  font-weight: 600;
  letter-spacing: -0.02em;
}
.proof-chart {
  margin-top: 18px;
}
.more {
  margin: 18px 0 0;
  font-size: 14px;
}
.more a {
  color: var(--rb-emerald-600);
  font-weight: 500;
}

.stats {
  max-width: var(--rb-container);
  margin: 56px auto 0;
  padding: 0 24px;
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  border-top: 1px solid var(--rb-line-soft);
  list-style: none;
}
.stats li {
  padding: 22px 20px 26px 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
  border-right: 1px solid var(--rb-line-soft);
}
.stats li:last-child {
  border-right: 0;
}
.stats strong {
  font-size: 19px;
  font-weight: 600;
  letter-spacing: -0.02em;
}
.stat-label {
  font-size: 13px;
  color: var(--rb-ink-2);
}
.stat-note {
  font-size: 12px;
  color: var(--rb-ink-3);
  line-height: 1.5;
}

/* ---------- 通用分区 ---------- */
.section {
  padding: var(--rb-section-y) 0;
}
.section.alt {
  background: var(--rb-bg-alt);
  border-top: 1px solid var(--rb-line-soft);
  border-bottom: 1px solid var(--rb-line-soft);
}
.inner {
  max-width: var(--rb-container);
  margin: 0 auto;
  padding: 0 24px;
}
.inner.narrow {
  max-width: 820px;
}
.head {
  max-width: 760px;
}
.eyebrow {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  font-weight: 500;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--rb-ink-3);
}
.eyebrow i {
  font-style: normal;
  color: var(--rb-emerald-600);
  font-family: var(--rb-font-mono);
}
.eyebrow.light {
  color: rgba(255, 255, 255, 0.55);
}
.display {
  margin: 14px 0 0;
  font-size: clamp(26px, 3.1vw, 38px);
  line-height: 1.28;
  letter-spacing: -0.025em;
  font-weight: 600;
}
.display.light {
  color: #f3f5f7;
}
.lead {
  margin: 14px 0 0;
  font-size: 15.5px;
  line-height: 1.8;
  color: var(--rb-ink-2);
}
.lead code,
.panel code,
.callout code {
  padding: 1px 5px;
  border-radius: var(--rb-radius-xs);
  background: rgba(0, 0, 0, 0.055);
  font-family: var(--rb-font-mono);
  font-size: 0.9em;
}

/* ---------- 对比表 ---------- */
.table-wrap {
  margin-top: 36px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  overflow: hidden;
  background: #fff;
}
table.compare {
  width: 100%;
  border-collapse: collapse;
  font-size: 14px;
}
.compare th,
.compare td {
  text-align: left;
  padding: 13px 16px;
  border-bottom: 1px solid var(--rb-line-soft);
  vertical-align: top;
}
.compare thead th {
  background: #fbfbfa;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--rb-ink-2);
  white-space: nowrap;
}
.compare tbody tr:last-child td {
  border-bottom: 0;
}
.compare tbody td:first-child {
  color: var(--rb-ink-3);
  white-space: nowrap;
}
.compare .accent-col {
  background: rgba(4, 120, 87, 0.035);
  color: var(--rb-ink);
  font-weight: 500;
}
.compare td code {
  font-family: var(--rb-font-mono);
  font-size: 12.5px;
}
.callout {
  margin: 18px 0 0;
  padding-left: 14px;
  border-left: 2px solid var(--rb-line-strong);
  font-size: 13.5px;
  line-height: 1.75;
  color: var(--rb-ink-2);
}
.callout a {
  color: var(--rb-emerald-600);
}

/* ---------- 能力卡 ---------- */
.grid {
  display: grid;
  gap: 16px;
  margin-top: 36px;
}
.grid > * {
  min-width: 0;
}
.grid.features {
  grid-template-columns: repeat(3, 1fr);
}
.card {
  position: relative;
  padding: 22px 22px 24px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
  transition: border-color 0.2s, box-shadow 0.2s, transform 0.2s;
}
.card:hover {
  border-color: var(--rb-line-strong);
  box-shadow: var(--rb-shadow-sm);
  transform: translateY(-2px);
}
.card-no {
  font-family: var(--rb-font-mono);
  font-size: 11.5px;
  color: var(--rb-emerald-600);
}
.card h3 {
  margin: 10px 0 8px;
  font-size: 15.5px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.card p {
  margin: 0;
  font-size: 13.5px;
  line-height: 1.72;
  color: var(--rb-ink-2);
}

/* ---------- 架构 ---------- */
.flow {
  display: flex;
  align-items: stretch;
  gap: 12px;
  margin-top: 36px;
}
.node {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 16px 18px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
}
.node.key {
  border-color: rgba(4, 120, 87, 0.35);
  background: rgba(4, 120, 87, 0.03);
}
.node-label {
  font-size: 11px;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--rb-ink-3);
}
.node strong {
  font-size: 15px;
  font-weight: 600;
}
.node small {
  font-size: 12.5px;
  color: var(--rb-ink-3);
}
.arrow {
  align-self: center;
  color: var(--rb-ink-3);
  font-size: 15px;
}

.tree {
  margin: 20px 0 0;
  padding: 20px 22px;
  border-radius: var(--rb-radius);
  background: var(--rb-code-bg);
  border: 1px solid rgba(0, 0, 0, 0.4);
  overflow-x: auto;
  font-family: var(--rb-font-mono);
  font-size: 12.5px;
  line-height: 1.78;
  color: #b7c2cd;
}
.tree code {
  font: inherit;
  white-space: pre;
}

/* ---------- SDK ---------- */
.grid.sdks {
  grid-template-columns: repeat(3, 1fr);
}
.sdk {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 20px 22px 22px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
  text-decoration: none;
  color: inherit;
  transition: border-color 0.2s, box-shadow 0.2s, transform 0.2s;
}
.sdk:hover {
  border-color: var(--rb-line-strong);
  box-shadow: var(--rb-shadow-sm);
  transform: translateY(-2px);
}
.sdk-lang {
  font-size: 13px;
  font-weight: 600;
  color: var(--rb-emerald-600);
}
.sdk code {
  font-family: var(--rb-font-mono);
  font-size: 12.5px;
  color: var(--rb-ink);
  background: rgba(0, 0, 0, 0.04);
  padding: 5px 8px;
  border-radius: var(--rb-radius-xs);
  align-self: flex-start;
}
.sdk p {
  margin: 0;
  font-size: 13.5px;
  line-height: 1.7;
  color: var(--rb-ink-2);
}

/* ---------- 部署 ---------- */
.grid.two {
  grid-template-columns: 1fr 1fr;
}
.panel {
  padding: 22px;
  border: 1px solid var(--rb-line);
  border-radius: var(--rb-radius);
  background: #fff;
}
.panel h3 {
  margin: 0 0 8px;
  font-size: 15.5px;
  font-weight: 600;
}
.panel p {
  margin: 0 0 14px;
  font-size: 13.5px;
  line-height: 1.72;
  color: var(--rb-ink-2);
}
.panel pre {
  margin: 0;
  padding: 16px 18px;
  border-radius: var(--rb-radius-sm);
  background: var(--rb-code-bg);
  overflow-x: auto;
  font-family: var(--rb-font-mono);
  font-size: 12px;
  line-height: 1.75;
  color: #b7c2cd;
}
.panel pre code {
  font: inherit;
  white-space: pre;
}
.table-wrap.images {
  margin-top: 16px;
}

/* ---------- FAQ ---------- */
.faq-list {
  margin-top: 30px;
  border-top: 1px solid var(--rb-line-soft);
}
.faq-item {
  border-bottom: 1px solid var(--rb-line-soft);
}
.faq-item summary {
  padding: 16px 2px;
  font-size: 15px;
  font-weight: 500;
  cursor: pointer;
  list-style: none;
  display: flex;
  justify-content: space-between;
  gap: 16px;
}
.faq-item summary::-webkit-details-marker {
  display: none;
}
.faq-item summary::after {
  content: '+';
  color: var(--rb-ink-3);
  font-weight: 400;
}
.faq-item[open] summary::after {
  content: '−';
}
.faq-item p {
  margin: 0 0 18px;
  font-size: 14px;
  line-height: 1.8;
  color: var(--rb-ink-2);
}

/* ---------- 收尾 ---------- */
.final {
  background: var(--rb-dark);
  padding: 88px 24px 92px;
}
.final-inner {
  max-width: 720px;
  margin: 0 auto;
  text-align: center;
}
.final-sub {
  margin: 16px 0 0;
  font-size: 14.5px;
  line-height: 1.8;
  color: rgba(255, 255, 255, 0.62);
}
.btn.primary.on-dark {
  background: #f3f5f7;
  color: #101418;
}
.btn.primary.on-dark:hover {
  background: #fff;
}
.btn.ghost.on-dark {
  background: transparent;
  border-color: rgba(255, 255, 255, 0.22);
  color: #e8edf2;
}
.btn.ghost.on-dark:hover {
  border-color: rgba(255, 255, 255, 0.45);
}

/* ---------- 响应式 ---------- */
@media (max-width: 1024px) {
  .proof {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .proof li:nth-child(2) {
    border-right: 0;
  }
  .hero-inner {
    grid-template-columns: 1fr;
    gap: 40px;
    padding-top: 56px;
  }
  .grid.features,
  .grid.sdks {
    grid-template-columns: repeat(2, 1fr);
  }
  .stats {
    grid-template-columns: repeat(2, 1fr);
  }
  .stats li:nth-child(2) {
    border-right: 0;
  }
}
@media (max-width: 720px) {
  .hero-inner {
    padding: 40px 20px 0;
  }
  .stats {
    margin-top: 40px;
    padding: 0 20px;
    grid-template-columns: 1fr;
  }
  .stats li {
    border-right: 0;
    border-bottom: 1px solid var(--rb-line-soft);
    padding: 16px 0;
  }
  .stats li:last-child {
    border-bottom: 0;
  }
  .inner {
    padding: 0 20px;
  }
  .grid.features,
  .grid.sdks,
  .grid.two {
    grid-template-columns: 1fr;
  }
  .proof {
    grid-template-columns: 1fr;
  }
  .proof li {
    border-right: 0;
    border-bottom: 1px solid var(--rb-line-soft);
    padding: 14px 0;
  }
  .proof li:last-child {
    border-bottom: 0;
  }
  .flow {
    flex-direction: column;
    align-items: stretch;
  }
  .arrow {
    transform: rotate(90deg);
    align-self: center;
  }
  .table-wrap {
    overflow-x: auto;
  }
  table.compare {
    min-width: 560px;
  }
  .terminal-body,
  .tree {
    font-size: 11.5px;
  }
  .final {
    padding: 64px 20px 68px;
  }
  .hero-facts {
    gap: 18px;
  }
}
</style>
