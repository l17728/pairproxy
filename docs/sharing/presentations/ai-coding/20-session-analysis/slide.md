<style>
.sast{font-size:clamp(.52rem,.72vw,.62rem);line-height:1.5;}
.sast table{border-collapse:collapse;width:100%;margin:.3vh 0;}
.sast th,.sast td{border:1px solid var(--bdr);padding:.25vh .4vw;text-align:left;}
.sast th{background:var(--bg2);font-weight:600;white-space:nowrap;}
.sast .r1{color:#ff6b6b;font-weight:700;} .sast .r2{color:#ffa94d;font-weight:700;}
.sast .r3{color:#ffd43b;font-weight:700;} .sast .r4{color:#69db7c;font-weight:700;}
.sast .r5{color:#4dabf7;font-weight:700;} .sast .r6{color:#9775fa;font-weight:700;}
.sast .r7{color:#e599f7;font-weight:700;} .sast .r8{color:#69db7c;font-weight:700;}
.sast .r9{color:#4dabf7;font-weight:700;} .sast .r10{color:#9775fa;font-weight:700;}
.sast .bar{display:inline-block;height:.8em;border-radius:2px;vertical-align:middle;}
.sast .diff-bad{color:#ff6b6b;} .sast .diff-good{color:#69db7c;}
</style>
<div class="sast">
<div style="display:flex;gap:.6vw;margin-bottom:.4vh;">
  <div style="flex:1;padding:.4vh .6vw;background:var(--bg2);border-radius:5px;border-left:3px solid #4dabf7;">
    <strong class="cb">数据源</strong><br>
    57 sessions · 834 messages · PairProxy(549) + ChatLab(279) + 其他(6)
  </div>
  <div style="flex:1;padding:.4vh .6vw;background:var(--bg2);border-radius:5px;border-left:3px solid #69db7c;">
    <strong class="cg">核心发现</strong><br>
    32% 质询确认 · 23% 测试验证 · 15% Review · 13% 文档同步
  </div>
</div>

<table>
<tr><th>#</th><th>模式</th><th>频次</th><th>占比</th><th>典型句式</th></tr>
<tr><td class="r1">①</td><td>质询确认</td><td>270 <span class="bar" style="width:90px;background:#ff6b6b;"></span></td><td>32%</td><td>「是否已经发布」「CI是否结束」「是否所有需求全部实现」</td></tr>
<tr><td class="r2">②</td><td>测试验证</td><td>194 <span class="bar" style="width:65px;background:#ffa94d;"></span></td><td>23%</td><td>「回归所有2000+测试」「请继续，直到全部通过」</td></tr>
<tr><td class="r3">③</td><td>Review/检查</td><td>128 <span class="bar" style="width:43px;background:#ffd43b;"></span></td><td>15%</td><td>「请组织专家对该设计进行review」「检视交付物」</td></tr>
<tr><td class="r4">④</td><td>文档更新</td><td>112 <span class="bar" style="width:37px;background:#69db7c;"></span></td><td>13%</td><td>「更新相应文档」「更新到CLAUDE.md」「写release notes」</td></tr>
<tr><td class="r5">⑤</td><td>Bug修复</td><td>96 <span class="bar" style="width:32px;background:#4dabf7;"></span></td><td>12%</td><td>「修复这个错误」「修正逻辑」</td></tr>
<tr><td class="r6">⑥</td><td>Commit&Release</td><td>92 <span class="bar" style="width:31px;background:#9775fa;"></span></td><td>11%</td><td>「commit & push & release & 写release notes」</td></tr>
<tr><td class="r7">⑦</td><td>举一反三</td><td>37 <span class="bar" style="width:12px;background:#e599f7;"></span></td><td>4%</td><td>「每一个bug都要举一反三，补充分的测试用例」</td></tr>
<tr><td class="r8">⑧</td><td>Spec/Plan</td><td>36 <span class="bar" style="width:12px;background:#69db7c;"></span></td><td>4%</td><td>「Implement the following plan: # ...」粘贴完整计划</td></tr>
<tr><td class="r9">⑨</td><td>统计数据</td><td>22 <span class="bar" style="width:7px;background:#4dabf7;"></span></td><td>3%</td><td>「cloc统计规模」「实际发送给LLM的统计」</td></tr>
<tr><td class="r10">⑩</td><td>CI检查</td><td>21 <span class="bar" style="width:7px;background:#9775fa;"></span></td><td>3%</td><td>「查看CI结果」「CI是否结束」</td></tr>
</table>

<div style="display:flex;gap:.6vw;">
  <div style="flex:1;padding:.4vh .6vw;background:var(--bg2);border-radius:5px;">
    <strong style="color:#ff6b6b;">典型用户 ❌</strong><br>
    <span class="diff-bad">一句话描述需求</span> → 信任AI输出 → <span class="diff-bad">修完即走</span> → <span class="diff-bad">偶尔commit</span> → <span class="diff-bad">单次会话</span>
  </div>
  <div style="flex:1;padding:.4vh .6vw;background:var(--bg2);border-radius:5px;">
    <strong style="color:#69db7c;">你的方式 ✅</strong><br>
    <span class="diff-good">粘贴完整Spec/Plan</span> → 每步验证 → <span class="diff-good">举一反三+补测试</span> → <span class="diff-good">commit+push+release+docs</span> → <span class="diff-good">跨session续接</span>
  </div>
</div>
<a class="vlink" onclick="openV('session-analysis.html')" style="margin-top:.4vh;">📄 完整分析报告（session-analysis.md）</a>
</div>
