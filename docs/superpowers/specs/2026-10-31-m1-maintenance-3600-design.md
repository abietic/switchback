# M1 私有维护 3600＋20 秒离线交付设计

状态：用户已接受 A。用户原话：“接受 A，调整为 3600＋20 秒，保留全部校验和其他预算，继续完成整片离线实现、验证与评审；本轮不进入真实环境。”此文落实已经批准的候选，不新增方向、权限或保证。

旧 `.scratch/runtime-m1-fd-maintenance-budget-1030` 的完整900秒联合验证失败；900.000068083秒耗尽动作，独立收尾0.037107792秒完成。旧目录、代码、两次失败和原始证据保持原样。新阶段为 `.scratch/runtime-m1-fd-maintenance-3600-1031`，使用独立的完整私有载体、取证代码、执行代次和候选绑定。

唯一行为变化是私有维护 work3600、cleanup20、total3620，policy `owned_fd_maintenance3600_v1`。cleanup 截止点仍是 `min(shared_start+3620, cleanup_start+20)`，不允许把剩余动作时间转给收尾，也不允许收尾继续工作。现有 private clock_control.v2 的严格字段、整数、一次使用和 deadline 验证保留；旧900 policy/绑定在新载体拒绝。正式 Source 的 Engine120、外层7200、资格240和barrier300，以及全部资源与输出预算保持。

Source、Go、模板、二进制和已封存的 kit-0001/bundle-0001 不重建。新载体同步所有实际 producer、consumer、程序、压缩包、provenance、验证器、最终证据及 disabled authorization，不能仅替换报告数字。未来执行路径固定为 `.scratch/runtime-m1-fd-maintenance-3600-execution-1031-01/carrier`，本轮不创建。旧归档目标及归属检查不扩展。

完整字节、哈希、双样本、namespace/owner、FD/maps/writer/manager/store、每次rename前后重验、全部历史与当前树读取均保留。宿主 checkpoint 前置读取1次和保存本体8轮分别记录，不能删除第9次真实读取。沿用已经评审的串行实现、真实native artifact端口和显式Linux/command模型；不引入并发、内容/hash/proof缓存或成功stub。

先通过小型合同/故障回归、实际程序argv上限及独立候选验证，再执行新的完整联合计时。native物化范围至少覆盖旧最终107809条artifact、4841635441字节、111631节点；候选字节变化按真实当前清单重新核对，而非为凑旧数字截断。未知历史内容保持逐叶同尺寸合成与原hash来源，已知ELF头和可恢复字节不变。计费覆盖真实OS read、EOF、失败前缀；异常后只在原清理/join返回后保存原内存明细。

成功必须真实到达完整保存、四份复制核验、Engine和22阶段qualification的实际Python控制流程；Linux内核、Go命令、PG、Engine服务仍为明确离线端口。完整journal、原45196帧对应、逐路径/阶段/轮次账和测前实现绑定必须可独立重算。work、cleanup和total均须有正余量；不得因新预算更大就先宣布通过。3600秒仍未验证，失败时保留新前缀并诊断，不自动加预算。

独立任务评审及最终整体评审无阻塞问题后，冻结唯一snapshot，创建无运行权限的离线approval/seal，再以外部hash只读验封。通过前操作清单只能是ineligible draft。真实VM、guest、Stop、对象移动、安装、凭据、provider与产品体验闭环均不在本轮权限内。
