# AI YouTube Shorts Radar

> Free AI-powered YouTube Shorts trend finder.
>
> 为做 Shorts 的人找下一个热点，不收费，不订阅，不用 LLM API Key。

## Demo

[![AI YouTube Shorts Radar demo](./demo.gif)](./demo.mp4)

做 YouTube Shorts，最难的往往不是剪视频，而是找下一个值得做的题。

**AI YouTube Shorts Radar** 就是为这件事做的:

- 输入一个种子关键词
- 用免费 AI 帮你扩更多方向
- 找出高播放、增长快、值得跟的 Shorts
- 结果本地保存，方便回看和复盘

## 它解决什么问题

很多 Shorts 选题流程都很痛苦:

- 手动搜关键词，慢
- 热点起得太快，容易漏掉
- 看到一个爆款，却不知道还能延伸什么方向
- 开了一堆标签页，最后还是找不到下一个题

这个项目的目标很直接:

**帮 Shorts 创作者更快找到下一个热点，而且免费。**

## 为什么值得用

- 为 Shorts 创作者做的，不是堆一堆复杂分析功能
- 重点不是只看老爆款，而是更快发现正在起量的内容
- 免费使用，不收费，不订阅
- 用 AI 帮你扩关键词，但不要求你为 AI 额外付费
- 研究结果会保存下来，不用每次重新找

## 适合谁

- 想更快找选题的 Shorts 创作者
- 做 faceless channel 的人
- 做短视频研究的工作室、运营、剪辑
- 不想为趋势工具每月付费的人

## 快速开始

现在已经有可用的 release 版本，支持 Windows 10 和 Windows 11。

如果你想从源码运行，需要:

- Windows 10/11
- Go 1.25+
- WebView2 Runtime
- 一个免费的 YouTube Data API Key

运行:

```powershell
go build -ldflags "-H windowsgui" -o .\bin\app.exe .\cmd\app
.\bin\app.exe
```

打开后:

1. 填入 YouTube API Key
2. 输入一个关键词
3. 让 AI 帮你扩方向
4. 看哪些 Shorts 正在起量

## Star Goals

- `500 Stars`: 增加 macOS 和 Linux 支持
- `2k Stars`: 做成在线版本

如果它对你有帮助，欢迎给我一个 Star。

## 一句话

如果你做 YouTube Shorts，这个项目只做一件事:

**帮你更快找到下一个热点，而且免费。**

[English](./README.md) | [中文](./README.zh-CN.md)

## License

MIT. See [LICENSE](./LICENSE).
