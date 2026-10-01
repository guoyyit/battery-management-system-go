# battery-management-system-go

一套**完全本地、确定性、可长时间运行**的电池管理系统（BMS），面向可配置串数
（N 串）与容量的多电芯串联电池组。电芯模型与电流/电压/温度输入全部由本地
模拟驱动，不依赖任何外部硬件或服务。

- 荷电状态（SOC）：库仑积分 + 静置开路电压（OCV）校正，应对电流传感器噪声与慢漂
- 被动均衡：按单体电压差或 SOC 差触发，旁路电流受限、开关与累计均衡电量可查询
- 动态电流限制：温度降额曲线可配置，叠加 SOC 与单体电压双向降额
- 保护与锁存：过/欠压、过温、充/放电过流、短路；去抖 + 滞回 + 显式清除
- 故障可追溯：带类型、单体、电压/温度快照与时间戳的有序、可翻页历史
- 并发一致性：采样 / 均衡 / 保护 / 查询共享一把锁，对外永远是自洽快照
- 崩溃恢复：SOC、均衡进度、保护锁存与故障历史原子持久化，重启后不丢不重
- 固定随机种子下，仿真与测试结果完全可复现

## 目录结构

```
bms/
  config.go        默认参数与跨字段校验
  ocv.go           分段线性 OCV↔SOC 曲线（默认 LFP 风格 3.0–3.45 V）
  model.go         串联电芯物理模型：容量/内阻/OCV 偏差、I²R 发热、噪声与漂移传感器
  soc.go           库仑积分 + 静置 OCV 校正
  balancer.go      被动均衡决策（滞回、并发数、电流上限、采样补偿）
  currentlimit.go  温度/SOC/单体电压动态电流限制
  protection.go    去抖、锁存、滞回清除
  fault.go         有序可翻页的故障历史
  storage.go       JSON 快照原子落盘（临时文件 + fsync + rename）
  bms.go           并发安全的编排核心（单 tick 有序状态机）
cmd/bmsim/         24 h 任务剖面的长时仿真驱动，演示恢复语义
```

## 运行与测试

```bash
go build ./...
go vet ./...
go test ./... -race -count=1          # 全部自动化测试（含竞态检测）

go run ./cmd/bmsim -duration 24h -state bms-state -seed 42
# 杀掉进程后用相同 -state 重新运行，会从最后一次快照恢复
```

## 仿真模型

- 每节电芯有独立容量 `CapacityAh`、内阻 `ResistanceOhm` 与 OCV 偏差 `VocBias`，
  默认制造散度由 `NewModel` 注入（初值 SOC 呈 ±3% 分布）。
- **电流符号约定：放电为正，充电为负。**
- 端电压 `V = OCV(SOC) + bias − R·I`，再叠加电压白噪声；温度用集总热模型
  `dT = k·I² − cooling·(T−T_amb)`。
- 电流传感器输出 = 真实电流 + 白噪声（`CurrentNoiseStd`）+ **慢速漂移**。
  漂移用均值回归（Ornstein–Uhlenbeck）过程建模，时间常数 2 h，平稳 RMS 由
  `CurrentDriftPerHour` 决定——它会造成库仑积分的缓慢累积偏差，由 OCV 校正消除。
- 随机性来自 `math/rand/v2` 的 PCG，种子由 `ModelConfig.Seed` 固定。

## SOC 估算

- 每节电芯独立积分：`SOC_i -= (I_sensed + I_bal,i)·dt / CapacityAh`，旁路电流
  只从正在均衡的电芯扣电。
- **静置判据**：`|I_sensed| ≤ RestCurrentThresholdA` 持续 `RestDuration`。
- 静置后按指数速率向 OCV 反推 SOC 收敛：
  `SOC += α·(SOC_OCV − SOC)`，`α = OCVCorrectionRate·dt`（默认 0.02/s，约 50 s
  时间常数）。这保证“静置后重新上电”时估计收敛到真实值。
- 单次静置的总修正量受 `MaxCorrectPerRest`（默认 15%）限制，拒绝单帧坏样本。
- 均衡中的单体端电压含旁路压降，校正时先加回 `I_bal·R_bypass`，因此**均衡
  进行中的电压采样仍然有效**。

默认噪声/漂移参数下，测试覆盖了数小时放电—静置—充电—静置循环，稳态误差 ≤ 5%。

## 被动均衡

- 两种触发模式：`BalanceByVoltage`（默认，基于补偿后 OCV）或 `BalanceBySOC`。
- 高者泄放：超过最低单体 `VoltageThresholdV` / `SOCThreshold` 的电芯闭合旁路，
  并设滞回（`Hysteresis`）防止阈值附近开关抖动。
- 约束：单路电流 `≤ MaxBalanceA`（默认 2 A，`I = Voc/R_bypass` 后再夹），
  同时均衡路数 `≤ MaxConcurrent`，仅在 `|I_pack| ≤ ArmCurrentA` 且接触器闭合时动作。
- `Status.Balance.Active / CurrentA / ChargeAh` 分别给出开关状态、当前旁路电流与
  累计均衡电量。

## 动态电流限制

每 tick 对请求电流（带符号）计算两个包络并取最严约束：

- **温度**：`ChargeTempCurve` / `DischargeTempCurve` 分段线性插值，曲线外按端点值
  钳死（0 = 禁止）。默认：充电 −10 ℃ 禁、0 ℃ 30%、25 ℃ 100%、50 ℃ 禁；
  放电 −20 ℃ 30%、25 ℃ 100%、60 ℃ 禁。
- **SOC**：高于 `SOCTaperHigh`（默认 0.95）线性限制充电；低于 `SOCTaperLow`
  （默认 0.05）线性限制放电，端点为零。
- **单体电压**：进入硬限上下 `VoltageBandV` 区间后线性降额，硬限处为零。

请求超限时按上限执行，`LimitResult.Limited` 置位，`Reasons` 给出每条生效约束。

## 保护与安全语义

| 故障 | 触发 | 清除（滞回） |
| --- | --- | --- |
| 过压 `FaultOvervoltage` | 任一 `V ≥ CellOverV`（3.65 V） | 全部 `V < CellOverVClear`（3.45 V） |
| 欠压 `FaultUndervoltage` | 任一 `V ≤ CellUnderV`（2.80 V） | 全部 `V > CellUnderVClear`（3.00 V） |
| 过温 `FaultOvertemp` | 任一 `T ≥ OvertempC`（60 ℃） | 全部 `T < OvertempClearC`（50 ℃） |
| 充电过流 | `I ≤ −OCPChargeA`（−110 A） | 电流回到正常窗口 |
| 放电过流 | `I ≥ OCPDischargeA`（120 A） | 同上 |
| 短路 | `I ≤ −ShortCircuitA`（−300 A） | 同上 |

- **去抖**：条件持续 `TripDebounce`（默认 500 ms）才锁存；短暂抖动（噪声尖峰、
  阈值附近来回穿越）不动作，也不会重复产生故障。
- **锁存**：一旦触发，立即断开充放电回路；条件自行恢复**不会**自动复位，只有
  显式调用 `BMS.ClearFaults` 且滞回条件满足才解除。
- **故障优先于计量**：每个 tick 先做保护判定（过流/短路检测基于**请求电流**，
  不受限流掩盖），锁存则本 tick 电流强制为零、旁路开关先于物理积分全部断开，
  因此不存在“保护已触发但电流仍继续计量”的中间态。

## 故障可追溯

每次锁存与每次显式清除都生成一条不可变 `FaultRecord`（单调递增 `Seq`、时间、
类型、相关单体、电压/温度/电流快照、Tripped/清除标志），按时间有序，支持
`FaultPage(offset, limit)` 翻页与 `SinceSeq` 增量拉取。

## 并发一致性

`BMS` 的全部可变状态由一把 `sync.RWMutex` 保护；`Step` 在锁内按固定顺序完成
“保护 → 限流 → 物理采样 → 均衡 → SOC → 快照 → 持久化”，`Status` /
`FaultPage` / `ClearFaults` 取读锁或写锁并返回深拷贝。并发测试在 `-race` 下
验证采样推进与外部查询永远看到自洽快照。

## 崩溃恢复

- 持久化内容：SOC、均衡开关与累计电量、当前锁存故障、完整故障历史、下一个序号。
- 写入方式：`state.json` 经临时文件 `write → fsync → rename → fsync(dir)` 原子
  替换，任意时刻被杀都只会看到上一份完整快照或新完整快照，不会出现半截 JSON。
- 触发时机：保护锁存 / 清除故障时**立即**落盘；平时按 `PersistEvery` 周期落盘；
  `Close()` 强制落盘。两次快照之间最多丢失 `PersistEvery` 内的库仑积分电量。
- 重启恢复后故障序号连续追加，历史既不重复也不遗漏；仿真真值同步到恢复的 SOC，
  使后续地面真值连续。

## 关键默认参数（4S / 100 Ah）

| 类别 | 参数 | 默认值 |
| --- | --- | --- |
| 模型 | 单体内阻 / 电流噪声 σ / 漂移 RMS | 2 mΩ / 0.1 A / 0.3 A |
| SOC | 静置电流阈值 / 静置时长 / 校正速率 | 2 A / 10 min / 0.02 s⁻¹ |
| 均衡 | 阈值（V、SOC）/ 滞回 / 单路电流 / 并发 | 15 mV、1%、5 mV、2 A、2 路 |
| 限流 | 充/放额定 | 100 A |
| 保护 | 去抖 / 过压 / 欠压 / 过温 / 短路 | 0.5 s / 3.65 / 2.80 V / 60 ℃ / 300 A |
| 持久化 | 周期 | 5 s（`bmsim` 10 s） |

所有参数见 `config.go` 的 `DefaultConfig()`，可用代码覆盖后经 `Config.Validate()`
做跨字段校验（串数、容量、降额曲线单调性、保护滞回关系等）。
