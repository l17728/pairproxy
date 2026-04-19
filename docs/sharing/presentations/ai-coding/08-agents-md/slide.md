<div class="split" style="margin-top:1vh;">
  <div class="split-half">
    <div style="padding:1vh 1.2vw;background:#ff6b6b10;border-radius:8px;border-left:3px solid #ff6b6b;margin-bottom:1vh;">
      <h4 class="cr" style="font-size:clamp(.72rem,1vw,.85rem);">❌ 不要写</h4>
      <p style="font-size:clamp(.65rem,.9vw,.75rem);">请认真编写测试 / 错误处理要友好 / 不要提交 Key</p>
    </div>
    <div style="padding:1vh 1.2vw;background:#69db7c10;border-radius:8px;border-left:3px solid #69db7c;margin-bottom:1vh;">
      <h4 class="cg" style="font-size:clamp(.72rem,1vw,.85rem);">✅ 应该写</h4>
      <p style="font-size:clamp(.65rem,.9vw,.75rem);">Fail-Open 原则 / 包装顺序不能改 / make test-race / Zap 四级日志</p>
    </div>
    <div class="mets">
      <div style="text-align:center;"><div class="met-v cr">50%→15%</div><div class="met-l">review 改动量</div></div>
      <div style="text-align:center;"><div class="met-v cg">7h→0</div><div class="met-l">并发 bug 复发</div></div>
    </div>
  </div>
  <div class="split-divider"></div>
  <div class="split-half">
    <div style="display:grid;grid-template-columns:1fr 1fr;gap:.5vw;font-size:clamp(.62rem,.88vw,.72rem);">
      <div style="padding:.5vh .8vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:6px;"><span class="cr">①</span> Build & Test</div>
      <div style="padding:.5vh .8vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:6px;"><span class="co">②</span> Code Style</div>
      <div style="padding:.5vh .8vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:6px;"><span class="cy">③</span> Testing 防回归</div>
      <div style="padding:.5vh .8vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:6px;"><span class="cg">④</span> Architecture</div>
      <div style="padding:.5vh .8vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:6px;"><span class="cb">⑤</span> Config & API</div>
      <div style="padding:.5vh .8vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:6px;"><span class="cp">⑥</span> 版本特性</div>
      <div style="padding:.5vh .8vw;background:var(--bg2);border:1px solid var(--bdr);border-radius:6px;grid-column:span 2;text-align:center;"><span class="ck">⑦</span> 复盘 & 举一反三</div>
    </div>
    <a class="vlink" onclick="openV('harness-layers.html')" style="margin-top:1vh;font-size:clamp(.6rem,.85vw,.72rem);">📊 五层文档体系</a>
  </div>
</div>
