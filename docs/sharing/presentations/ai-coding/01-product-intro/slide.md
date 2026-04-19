<div class="stitle" contenteditable="true"><br></div><div class="stitle" contenteditable="true"><br></div><p class="sub" contenteditable="true">企业级 LLM API 网关 —— Claude Code / Opencode / Openclaw 的统一接入层</p>
<div class="split" style="margin-top:1vh;">
  <div class="split-half">
    <div style="padding:1.2vh 1.2vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:8px;margin-bottom:1vh;">
      <h4 class="cb" contenteditable="true">解决的问题</h4>
      <p contenteditable="true">企业多用户共享 LLM API Key 的认证、配额、审计、高可用问题</p>
    </div>
    <div style="padding:1.2vh 1.2vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:8px;">
      <h4 class="cg" contenteditable="true">架构</h4>
      <p style="font-family:var(--mono);font-size:clamp(.65rem,.9vw,.75rem);" contenteditable="true">Claude Code → sproxy(网关) → Anthropic / OpenAI / Ollama</p>
    </div>
  </div>
  <div class="split-divider" contenteditable="true"></div>
  <div class="split-half">
    <div style="padding:1.2vh 1.2vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:8px;margin-bottom:1vh;">
      <h4 class="co" contenteditable="true">7 层功能</h4>
      <p contenteditable="true">核心引擎 · 协议转换 · 认证安全 · 多租户 · 可观测性 · 集群 · 工具链</p>
    </div>
    <a class="vlink" onclick="openV('feature-radial.html')" contenteditable="true">📊 特性全景图</a>
    <a class="vlink" onclick="openV('readme-viewer.html')" style="margin-top:.5vh;" contenteditable="true">📄 项目 README</a>
  </div>
</div>
