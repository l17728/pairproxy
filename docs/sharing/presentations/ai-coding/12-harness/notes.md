我一直在说"脚手架"，这里展开讲讲到底是什么。

一个 Coding Agent，本质上等于 AI 模型加上你给它搭的 Harness。Harness 定义了 AI 能看到什么、能用什么工具、怎么被验证。

六个面：Context Files 注入知识、Tools/MCP 扩展能力、Sub-agents 做上下文隔离、Skills 按需加载、Hooks 做自动化流程、Back-pressure 做强制验证。

我想说一个可能反直觉的观点：**大多数人把时间花在"怎么跟 AI 说话"上——调 prompt、找技巧。但真正决定 AI 产出质量的，是你给它搭的 Harness。**

同样的模型，好的 Harness 和差的 Harness，产出质量天差地别。
