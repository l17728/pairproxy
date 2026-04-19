<hr class="hrd">
<div class="split">
  <div class="split-half">
    <h3 class="co">Spec 四要素</h3>
    <div style="font-size:clamp(.65rem,.92vw,.78rem);">
      <div style="padding:.6vh .8vw;background:var(--bg2);border-radius:5px;margin-bottom:.4vh;">🎯 用具体反例描述问题</div>
      <div style="padding:.6vh .8vw;background:var(--bg2);border-radius:5px;margin-bottom:.4vh;">🚫 明确写出已拒绝方案及原因</div>
      <div style="padding:.6vh .8vw;background:var(--bg2);border-radius:5px;margin-bottom:.4vh;">📊 量化 Success Criteria（四维）</div>
      <div style="padding:.6vh .8vw;background:var(--bg2);border-radius:5px;">🚧 Out of Scope 排除范围</div>
    </div>
  </div>
  <div class="split-divider"></div>
  <div class="split-half">
    <h3 class="cy">TDD 循环</h3>
    <div class="flow">
      <div class="fstep" style="background:#ff6b6b15;"><span class="cr">N.1</span> 写失败的测试</div>
      <div class="farrow">↓</div>
      <div class="fstep" style="background:#ffa94d15;border:2px solid #ffa94d44;"><span class="co">N.2</span> <strong>验证它确实失败</strong></div>
      <div class="farrow">↓</div>
      <div class="fstep" style="background:#69db7c15;"><span class="cg">N.3</span> 实现功能代码</div>
      <div class="farrow">↓</div>
      <div class="fstep" style="background:#4dabf715;"><span class="cb">N.4</span> 验证通过 + 全包回归</div>
    </div>
  </div>
</div>
