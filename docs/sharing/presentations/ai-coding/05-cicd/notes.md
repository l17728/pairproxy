这个流程图是我用 Mermaid 画的 CI/CD 流水线。git push 自动触发 build、vet、test、lint，打 tag 自动交叉编译 5 个平台，同时构建 Docker 镜像推到 ghcr.io。

我想特别说一下，**CI/CD 在 AI Coding 里不是"加分项"，是必需品。**

为什么？传统开发的时候，你写了代码，你手动跑一下测试，大致心里有数。但 AI Coding 里，你没看过代码。你的"心里有数"从哪来？

从 CI 来。CI 是你的第二双眼睛。每次 AI 提交，CI 替你跑 vet、跑 race detect、跑 lint。绿色通过了，你才能松一口气。

所以如果你今天就要开始用 AI Coding，第一件事不是写代码，是先把 CI 搭好。
