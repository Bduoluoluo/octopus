# 三协议迁移：SSE 契约与性能实测

测量日期：2026-10-10（Asia/Shanghai）。本页性能主表为 **FINAL freeze 快照复测**：HEAD `fec9b361a4eca4e6a3ea47af00b67a1e392efa42` 加冻结的 P6 跨协议原生能力修复。使用原 baseline 二进制和重新冻结的 current 二进制，按相同流程完成三轮交替测量。初测摘要和原始记录保留在后文，未混入最终中位数。

最终快照中，Chat/Responses 请求转换耗时分别约为 baseline 的 **5.97× / 6.14×**，分配量也增加。4096 个文本 delta 的 live retained heap 分别为 Chat **2.490 → 6.273 MiB**、Responses **2.989 → 12.847 MiB**、Anthropic **2.427 → 4.612 MiB**。任务书没有性能硬阈值；这些回退如实记录，不据此虚报无回退或擅自判定整体迁移合格/不合格。本测试任务没有修改执行循环、协议业务实现或 benchmark 方法，也没有尝试性能优化。

## 版本与环境

| 项目 | 实际值 |
|---|---|
| baseline | `98c380bc205b673f3f9c42100ba68397df7a9947` 的归档 `.tmp/protocol-migration/baseline` |
| current HEAD | `fec9b361a4eca4e6a3ea47af00b67a1e392efa42`，加冻结的 P6 跨协议原生能力修复 |
| current 编译快照时间 | `2026-10-09T20:29:05.0035997Z`（本地 2026-10-10 04:29:05）；源文件清单与校验见日志 |
| 编译器 | 两侧均为 `go version go1.25.0 windows/amd64` |
| 机器 | Intel Core i7-14700F，Windows amd64，GOAMD64=v1，CGO_ENABLED=1 |
| 运行配置 | GOMAXPROCS=2，GOGC=100，GOMEMLIMIT=off；性能测试不启用 race |
| 重复方式 | 三轮，顺序 baseline/current、current/baseline、baseline/current |
| 时间预算 | 请求/首帧每个子测试 300ms；堆测试每个子测试 1 个完整流，三轮重复 |

初测时只把两份 benchmark 测试复制到 baseline，未改归档业务源码或 go.mod；两边测试源 SHA256 一致。FINAL 沿用该 baseline 二进制，没有重建或覆盖它；两个 benchmark 源哈希也与初测完全相同。重新编译 current 前后以及全部 benchmark 完成后，对 `go.mod`、`go.sum`、全部 `internal/**/*.go`（含测试）比较 SHA256，均保持稳定。固定二进制、编译器和源码清单可追溯；哈希范围不包括模块缓存、标准库及仓库外依赖文件。

FINAL current 源文件清单：`.tmp/protocol-migration/perf-final-current-sources.sha256`。**该清单文件本身的 SHA256** 为 `65C489ED1F07D2BC73662A1233FF944B7ECE0A6BA2E44A9F921CC19C14B21BE3`，用来标识本次测量的实际源码集合，而不是仅凭 HEAD 标识含未提交改动的工作树。

测试源：

- [SSE 契约与 fuzz](../internal/relay/stream/sse_contract_fuzz_test.go)
- [请求 benchmark](../internal/transformer/protocol_benchmark_test.go)
- [首帧与流式堆 benchmark](../internal/transformer/protocol_stream_benchmark_test.go)

二进制 SHA256：

- FINAL current（`perf-final-current.test.exe`）：`83463C964ADD27F96981FDCA764DAD07BD743A2449200CA0DE10D30FC8527BD2`
- baseline：`A952019344646E408E0CEC2E2077B2BAC1BAF918CBD4091D2F4C008BAB7CF64A`

## SSE 契约与有限 fuzz

本节为初测阶段已完成的验证记录；本次 FINAL 指令只要求性能复测，没有重新运行 fuzz/race，不把旧测试结果标为最终源码验收。

复用生产的 `FramedSSESource` 与 `github.com/tmaxmax/go-sse`，没有在测试中另写 SSE parser。

| 覆盖项 | 断言 |
|---|---|
| 分段/合并读取 | 相同字节经一次性读取、单字节读取和可变分段读取，得到相同 raw frames 和成熟 parser 的 event/data/ID |
| LF、CRLF、CR | 原始字节拼接完全等于输入；不丢帧、不重复帧 |
| 多行 data | 成熟 parser 按规范用 LF 合并 data，即使输入为 CRLF |
| 注释、event-only、ID | 注释不变成业务 event；event-only 可被解析；整流 parser 保留/清空 LastEventID |
| 非法 JSON | SSE 层保留原始 data，三协议 codec 报错，经 StreamProcessor 传播；不得写出业务 payload 或把错误降为“空流” |
| 大小限制 | 缩小到 1024 字节的限制下，64 字节 data 可读、8192 字节单行 data 被 source/parser 拒绝；多种 read 分段均一致 |
| 取消 | 已阻塞的 reader 在 processor 取消后由 source Close 解锁；返回 context.Canceled，无业务输出；Close 幂等，取消 context 在下一次读前被检查 |

`FuzzSSESegmentedCoalescedRead` 使用五个 seed，变异 body、分段计划和 1024–9215 字节解析限制；body 限制为 16 KiB，分段计划最多 256 字节。分别比较整流 mature parser、raw frame source，以及每帧再次分段后的解析结果。raw 成功读取时额外验证所有 frame 拼接等于原始输入。

正式执行：`-fuzztime=10s -parallel=2`，实际进程约 **11.021s**，**4757 次执行**，新增 **9** 个 interesting inputs，PASS。输出后半段计数没有继续增长；如实保留原始日志，不把 interesting inputs 当作失败或覆盖率百分比。有限 fuzz 没发现反例，不证明所有任意输入安全，也不覆盖完整 32 MiB 默认上限。

`go test -race ./internal/relay/stream -run '^TestSSE|^FuzzSSE' -count=1` 通过。这里的 fuzz 是 SSE/分段契约测试，不替代 JSON union、工具参数、citations 或完整路由 fuzz。

## 请求吞吐与分配

每次创建新的 inbound/outbound，解码请求到现有 IR，构建同协议 HTTP 请求，并把 Body 读到 `io.Discard` 后关闭。使用已有小型 system/user/function-tool fixture；没有网络请求、路由、Key、数据库或日志。三次测量取每列中位数。请求/s 为 `1e9 ÷ 中位数 ns/op`，是单个 benchmark 循环的本地处理速率，不是服务端 QPS。

| 协议 | baseline µs/op | current µs/op | baseline 请求/s | current 请求/s | baseline B/op / allocs | current B/op / allocs | 耗时倍数 |
|---|---:|---:|---:|---:|---:|---:|---:|
| chat | 5.276 | 31.520 | 189538 | 31726 | 4602 / 44 | 20614 / 268 | 5.97× |
| responses | 13.088 | 80.379 | 76406 | 12441 | 12431 / 165 | 60255 / 744 | 6.14× |
| anthropic | 136.198 | 166.753 | 7342 | 5997 | 178517 / 2381 | 199316 / 2631 | 1.22× |

## 本地首业务文本帧

计时从创建新适配器开始，经过 mature SSE parser、前导事件、第一条含文本的 delta，直到目标协议编码输出中出现固定文本标记；包含随后的 codec Close 调用。输入只有前导与第一条 delta，**不提供后续 terminal**，因此测试会在拿到首条文本后立即结束；等待完整流的实现无法通过。两边都走兼容的 `TransformStreamEvent` 方法，新版该方法委托帧状态机。所有 JSON fixture 自带 type，不包含仅依赖 SSE event 名称的性能案例。

这是内存中的本地转换耗时，包含不同协议各自所需的前导帧数，不是网络 TTFT、HTTP/WS executor 延迟或 `guardedStreamTransform` 的端到端测量。

| 协议 | baseline µs/op | current µs/op | baseline B/op / allocs | current B/op / allocs | 耗时倍数 |
|---|---:|---:|---:|---:|---:|
| chat | 9.150 | 32.979 | 10586 / 74 | 27633 / 297 | 3.60× |
| responses | 14.571 | 48.070 | 20102 / 164 | 41437 / 557 | 3.30× |
| anthropic | 9.458 | 21.213 | 14018 / 115 | 24720 / 234 | 2.24× |

## 流式存活堆与采样峰值

每个流包含 256 或 4096 个 **33 字节**文本 delta（分别 8.25 KiB / 132 KiB 纯文本），加协议前导、usage 与合法 terminal。Responses terminal 携带完整 output 快照，Chat 包含 finish_reason 后的 usage 快照及 [DONE]。三协议结束后均检查聚合文本准确性和 completion tokens，丢内容、重复内容或 usage 不正确会使 benchmark 失败。

输入 wire body 预先构建，暖身一次，然后 GC 后读取初始 HeapAlloc。每个流使用新适配器，输出字节立即丢弃，不积攒客户端输出副本。每 64 个 SSE event、流结束以及最终聚合校验后调用 `runtime.ReadMemStats`。

- `retained-B/op`：协议结束、GetInternalResponse 之前，强制 GC 后 HeapAlloc 相对初值的正增量；适配器由 KeepAlive 保持可达。它测量**仍存活的单个流状态**，不是请求销毁后的泄漏。
- `peak-sampled-B/op`：上述采样点中最大 HeapAlloc 减去初值。它包含当时尚未回收的垃圾及最终聚合/校验开销，可能漏掉采样点之间的真实高峰；不是精确峰值，也不是 RSS。
- 为减少测试成本，堆测试每次只有一个流；报告三轮中位数及长流范围。原始日志中的 ns/op 受到 ReadMemStats、GC 和计时开关影响，不用于吞吐或延迟比较。原始 B/op 只覆盖计时开启区间，最终 GetInternalResponse 校验在计时关闭后，因此不能与 peak 直接等同。

| 协议 / delta 数 | baseline retained MiB | current retained MiB | baseline peak-sampled MiB | current peak-sampled MiB |
|---|---:|---:|---:|---:|
| chat / 256 | 0.156 | 0.397 | 1.274 | 2.915 |
| chat / 4096 | 2.490 | 6.273 | 9.527 | 12.911 |
| responses / 256 | 0.210 | 0.828 | 2.315 | 2.945 |
| responses / 4096 | 2.989 | 12.847 | 10.888 | 28.356 |
| anthropic / 256 | 0.153 | 0.296 | 1.271 | 2.561 |
| anthropic / 4096 | 2.427 | 4.612 | 9.464 | 12.846 |

长流采样范围体现 GC 时机的波动：

| 协议 / 4096 delta | baseline peak 范围 MiB | current peak 范围 MiB | baseline retained 范围 MiB | current retained 范围 MiB |
|---|---:|---:|---:|---:|
| chat | 5.544–84.317 | 11.324–21.677 | 2.489–2.490 | 6.272–6.273 |
| responses | 8.732–97.918 | 28.331–33.127 | 2.856–3.255 | 12.847–12.847 |
| anthropic | 8.136–57.191 | 12.580–15.768 | 2.426–2.427 | 4.612–4.612 |

FINAL retained 随流长度增加，4096 delta 相比 256 delta 约增加到 15–16 倍；这组 fixture 不支持“内存与流长度无关”的结论。Responses 两侧计时区间总分配都很高（4096 delta 中位数 baseline 310671832 B/op、current 327497832 B/op），但累计分配不等于同时驻留内存。baseline 三种长流的单次 peak-sampled 分别出现约 84、98、57 MiB 高值，全部保留在原始记录和范围表，未删样本或另跑挑选更低结果。仅三次、低频采样且包含最终聚合校验，峰值受 GC/调度影响，不能据此得出最终版峰值比基线更低的结论。本任务没有运行 profile 来定位分配来源，不能只凭此表断言某个字段或某段代码是根因。

## 初测记录（preliminary）

初测 current 为 HEAD `9746da0035e80b82666d48d255e8b154e0798fd7` 加当时未提交迁移，时间 `2026-10-09T19:55:20Z`。以下仅保留原比较摘要；正式表格全部使用 FINAL 的新一轮 baseline/current 数据。

| 协议 | 初测请求耗时倍数 | 初测首帧耗时倍数 | 初测 4096 delta retained MiB（baseline → current） |
|---|---:|---:|---:|
| chat | 6.07× | 3.10× | 2.490 → 5.930 |
| responses | 6.40× | 3.49× | 2.856 → 12.785 |
| anthropic | 1.23× | 2.14× | 2.426 → 4.549 |

原 current 二进制 `perf-current.test.exe` 仍保留（SHA256 `49C342B366A67E3F06BC93D9E316E5B6CAB21025F74BA4AF17ECCB163461B96A`），原环境、源码清单及 `perf-{baseline,current}-{latency,heap}-{1,2,3}.log` 均未覆盖。

## 原始记录与复现

所有日志和冻结二进制保存在本地 `.tmp/protocol-migration`（没有修改父任务 manifest）。FINAL 编译退出码为 0；12 次正式 benchmark 调用均 PASS，36 组请求/首帧测量和 36 组堆测量完整。最终日志：

- [FINAL 环境和时间](../.tmp/protocol-migration/perf-final-environment.log)
- [FINAL current 源文件清单](../.tmp/protocol-migration/perf-final-current-sources.sha256)
- [FINAL 二进制/源码清单/benchmark 哈希](../.tmp/protocol-migration/perf-final-artifacts.sha256)
- [FINAL 编译及相邻源码检查](../.tmp/protocol-migration/perf-final-current-build.log)
- [测量后源码稳定及 baseline 哈希检查](../.tmp/protocol-migration/perf-final-postcheck.log)
- `perf-final-{baseline,current}-latency-{1,2,3}.log`
- `perf-final-{baseline,current}-heap-{1,2,3}.log`

初测记录（preliminary）及此前 SSE 验证日志：

- [环境与源快照时间](../.tmp/protocol-migration/performance-environment.log)
- [current 源文件 SHA256](../.tmp/protocol-migration/perf-current-sources.sha256)
- [两侧 benchmark 源文件 SHA256](../.tmp/protocol-migration/perf-benchmark-sources.sha256)
- [测试二进制 SHA256](../.tmp/protocol-migration/perf-binaries.sha256)
- [SSE fuzz 原始日志](../.tmp/protocol-migration/sse-fuzz.log)
- [SSE 契约 race 日志](../.tmp/protocol-migration/sse-contract-race.log)
- [整个 stream 包 race 日志](../.tmp/protocol-migration/sse-stream-suite-race.log)（全部测试通过）
- `perf-{baseline,current}-latency-{1,2,3}.log`
- `perf-{baseline,current}-heap-{1,2,3}.log`
- `perf-{baseline,current}-build.log`

从仓库根目录设置同一工具链：

```powershell
$env:GOTOOLCHAIN = 'go1.25.0'
$env:GOCACHE = Join-Path (Get-Location) '.gocache'
$env:GOTELEMETRY = 'off'
$env:GOMAXPROCS = '2'
$env:GOGC = '100'
$env:GOMEMLIMIT = 'off'
go test -mod=readonly ./internal/relay/stream -run '^$' -fuzz '^FuzzSSESegmentedCoalescedRead$' -fuzztime=10s -parallel=2
go test -mod=readonly -race ./internal/relay/stream -run '^TestSSE|^FuzzSSE' -count=1
go test -mod=readonly -c -o .tmp/protocol-migration/perf-final-current.test.exe ./internal/transformer
```

本次 FINAL 直接使用已保留的 `perf-baseline.test.exe`，先校验其 SHA256，不重新构建。若在另一环境从零复现，可把两份 benchmark 测试源原样放入 baseline，在归档根目录用同样编译器构建 `../perf-baseline.test.exe`，GOCACHE 保持仓库根目录绝对路径。正式测量命令：

```powershell
& .tmp/protocol-migration/perf-baseline.test.exe '-test.run=^$' '-test.bench=^BenchmarkProtocol(Request|FirstFrame)$' '-test.benchtime=300ms' '-test.count=1' '-test.cpu=2'
& .tmp/protocol-migration/perf-final-current.test.exe '-test.run=^$' '-test.bench=^BenchmarkProtocol(Request|FirstFrame)$' '-test.benchtime=300ms' '-test.count=1' '-test.cpu=2'
& .tmp/protocol-migration/perf-baseline.test.exe '-test.run=^$' '-test.bench=^BenchmarkProtocolStreamHeap$' '-test.benchtime=1x' '-test.count=1' '-test.cpu=2'
& .tmp/protocol-migration/perf-final-current.test.exe '-test.run=^$' '-test.bench=^BenchmarkProtocolStreamHeap$' '-test.benchtime=1x' '-test.count=1' '-test.cpu=2'
```

正式结果是三轮交替执行；单次预跑的数据没有混入表格。只采样三次，没有统计显著性检验，没有 CPU 绑核或排除系统后台/其他代理负载。堆指标是整个测试进程的差值，可能包含 runtime/sync.Pool 影响；校验构造预期文本也计入最后一个 heap 采样点。测试不包含并发请求、长 prompt、多模态/并行工具、raw passthrough、relay 原始日志缓冲、计费、网络及数据库，所以不能推导部署内存上界或直接解释 1 GiB RSS。FINAL 仅对应本页记录的 freeze 源码集合；以后修改 runtime/codec 或 benchmark 后应重新冻结并复测。本轮没有重跑完整功能测试、全仓 race 或前端构建，这些结果由父任务单独记录。
