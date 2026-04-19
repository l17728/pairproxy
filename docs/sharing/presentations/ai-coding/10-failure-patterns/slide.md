<a class="vlink" onclick="openV('failure-patterns.html')">📊 雷达图交互页</a>
<hr class="hrd">
<div class="cards2 stagger" style="max-width:1000px;">
  <div class="card" style="border-left:3px solid #ffa94d;">
    <h4><span class="co">降级策略过严</span> <span style="font-size:.65rem;color:var(--dim);">v2.9.1</span></h4>
    <p>thinking 参数 → 返回 400 → Claude Code 罢工。<strong>修复：</strong>静默剥离参数继续转发。</p>
  </div>
  <div class="card" style="border-left:3px solid #ff6b6b;">
    <h4><span class="cr">缓存安全假设</span> <span style="font-size:.65rem;color:var(--dim);">v2.9.3</span></h4>
    <p>JWT 缓存命中不校验活跃状态 → 禁用操作 24h 无效。<strong>修复：</strong>缓存命中后额外查 IsUserActive。</p>
  </div>
  <div class="card" style="border-left:3px solid #9775fa;">
    <h4><span class="cp">数据库语义盲区</span> <span style="font-size:.65rem;color:var(--dim);">v2.14.0</span></h4>
    <p>ON CONFLICT(id) 用 UUID → 节点间不同 UUID → 同步失效。<strong>修复：</strong>改用 ON CONFLICT(url)。</p>
  </div>
  <div class="card" style="border-left:3px solid #ffd43b;">
    <h4><span class="cy">算法属性副作用</span> <span style="font-size:.65rem;color:var(--dim);">v2.15.0</span></h4>
    <p>HMAC 确定性 → "重新生成" 返回相同 Key。<strong>防护：</strong>Spec 里单独列"用户行为预期"。</p>
  </div>
</div>
