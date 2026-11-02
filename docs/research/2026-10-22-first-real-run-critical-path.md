# Switchback 首次实跑：当前进度与剩余关键路径

_截至2026-10-26：显式执行配置、完整kit20/bundle1和准备器离线交付通过；最终整片复审C0/I0/M0。源码四包90主测试/904子测试、准备器102项及实际Go最终绑定零SKIP通过。Engine ready、资格首启和真实Task仍未完成。_

## 当前交付：kit20 完整离线验证与复审通过

phase `runtime-m1-worker-execution-profile-1025` 完成私有profile、完整Backend env、Create双网络和installer接线，15个Source/test文件冻结。完整原builder产出kit20与canonical bundle1；1086 Source/7模板、20二进制、完整覆盖/反向绑定/13 runtime null/四项authority false已核验。四包90主测试/904子测试、双Linux编译及实际最终Go绑定1PASS/0SKIP通过；准备器最终102项通过。源码、补修、交付及最终整片复审均Approved C0/I0/M0。默认关闭与原资源、capacity/PG/Trust/quota/Start边界保持；本轮无真实key、guest、环境或provider动作。

当前交付摘要 (private evidence retained)；最终整片复审 (private evidence retained)；单次真实准备与首启范围 (private evidence retained)。安装器漏项与后续阶段manifest provenance缺口均已离线RED/GREEN、修复及复审；kit19和原2727项版本通过404份before映射保留。当前canonical为`a77c1f154de9384cfbb35f0d4dd8f76147bbc7b6e8fb8b8bbfdfe6f7586de1d7`。已选DeepSeek，私有文件路径和真实授权缺失，模板继续null/false。

下一步是在一份具体授权下完成真实准备、Engine及资格首启，再验证一条DeepSeek Task的正式报告、页面接受和同任务观测，首条闭环后定向精简。真实Linux路径、Moby双网络、空PG无主动模型请求、PG原子准入/Native freshness/exact-CID quota/Start-Prompt仍须现场证据；原120/240/300秒是窗口界限，不是成功工期。完整证据及最终封存见本phase的completion.json/phase-seal.json。

## 最新离线推进：完整执行器通过，显式执行配置已获准

phase runtime-m1-kit18-preparation-operator-1025将五个guest安装单位、四旧kit17副本、4858叶consumed host stage、3372叶retained stage和七处archives接入具体全集预检、no-replace保留、copy和原Engine/条件首启顺序。完整回归80项及verifier通过；任务和整片最终复审Approved、C0/I0/M0。Source、kit18、原120/240/300秒门禁不变，本轮未进入guest/启动服务或生成秘密。

评审中发现并已离线关闭recorder后的deadline间隙、自身/proc FD枚举、审计聚合/写后失败漏计和六个必需boot parent预检/跨阶段identity绑定。原始失败、RED/GREEN、旧freeze与canonical均保留，不把修复后的评审倒写给历史版本。部署仍是owner冻结模型，跨host/guest非事务；阶段间漂移可导致host partial保留并在下一guest阶段拒绝，无rollback或重复探测。

旧kit18中的qualificationBackendEnvironment固定SWITCHBACK_M1_WORKER_DELIVERY_ENABLED=0，localproduct只在ExecutionEnabled为真时组装RuntimeRunner。因此kit18环境首启成功也不代表真实Worker交付。用户call_648b90b2fd0944ac9ba30e8325224fdd已接受显式执行profile设计：保持默认关闭与PG/Trust/容量/quota/Start，只在明确选择完整profile时绑定实际Worker image、Backend私有credential paths和已有control网络；先完成Source、新完整候选与离线交付，不读key、不启动环境、不调用provider。

前一执行器封存和新配置Source已完成；完整新候选与准备器通过后，审阅具体真实环境准备/首启，再以一条真实Task的report、页面accept和同任务观测关闭体验验收，随后定向精简。当前没有支持“若干分钟必过”的现场证据；Engine120秒和资格240秒是执行截止界限，不能涵盖首次镜像构建/PG准备或未知运行故障。

执行器记录 (private evidence retained)；整片复审 (private evidence retained)；已接受执行配置 (private evidence retained)。

## 最新只读核验：两个目录身份、归属及稳定性通过

用户call_294bafe24c70490d919a3b55ee19939d授权修正后的单次60秒只读窗口。新phase runtime-m1-installed-directories-readonly-corrected-1025核对31491旧封存记录、1998工作树基线和HEAD/index；复用逐字节不变的修正guest读器，内存回归重新通过。一次guest读取实际0.779778291秒exit0，无stderr、timeout、truncation或追加guest调用。

`/var/lib/switchback/m1-engine` identity为`[64769,307806,0,0,448]`，`/run/switchback-engine/m1-resident`为`[48,40,0,0,448]`，均root:root0700。stage仅有claim.json（2001bytes）和outcome.json（2627313bytes），均root:root0600、regular、单link、同FS，严格绑定旧kit17窗口；outcome的稳定字段、4835原始观测序列及hash匹配，离线重建完整JSON语义hash也匹配。现场原始字节hash与宿主重编码hash分别保留，不把重编码当原始字节；首次封存脚本相应断言错误及修订已保留，不涉及guest重跑。runtime为空。

前后boot均751079a6-74a4-4570-bca6-18e460c7f33d，目录/叶文件/父目录/挂载前后稳定。126进程/472FD中相关两个FD均只读，相关mapping为空，未观察到写引用。未修改guest文件、调用服务/Engine/PG/provider、启动VM或重试。Source和kit18不变。

两处此前未观测的目录身份与归属缺口现已关闭；其余安装文件、manager及其他副本/archive未在本窗口重查。下一步是把该snapshot接入具体保留/kit18切换执行器并完成离线检查，真实归档/切换及Engine/条件首启需具体范围，写入前仍重验现场。本次只读授权不产生下一环境窗口权限。

本次核验记录 (private evidence retained)；归属证据 (private evidence retained)。这一步完成的是身份取证，Engine部署、首启、Create/quota/Start和真实体验闭环仍待实跑。

## 历史只读核验：stage身份已观测，内容检查提前拒绝

用户明确授权两个目录的一次只读现场核验。新phase runtime-m1-installed-directories-readonly-1025执行前核对31442旧封存记录、1998基线和HEAD/index；一次60秒窗口实际0.476377458秒exit1，VM已Running、boot仍751079a6-74a4-4570-bca6-18e460c7f33d，guest一次进入。

已直接观测/var/lib/switchback/m1-engine identity为[64769,307806,0,0,448]（root:root0700），随后内容检查报unexpected/unsafe directory content并按unknown停止。具体叶stat未在拒绝前记录，现场触发字段不能确定；完整claim/ownership、第二个runtime目录身份和后续writer/稳定性核验均未完成。没有guest写文件、服务/Engine/PG/provider动作、VMstart、失败后核查、retry或remoteStop。

离线确认取证器契约遗漏：256KiB叶上限不支持生产preserve写入的outcome.json。原始wire恢复4835观测且序列hash匹配，重建producer outcome为2627313字节；在原directory_snapshot seam确定复现拒绝。这证明取证器不兼容正常输出，不冒充本轮未采到的具体leaf事实。kit18与产品源码保持封存，无需重建候选。

未执行的修正读器先记录两个目录和每叶stat，再有界读取/校验，支持保存outcome的稳定字段与原始观测hash，单叶66MiB/总68MiB；原root/single-link/nofollow/noatime/同FS/漂移/writer/boot边界保持。内存回归旧RED、新大outcome/空runtime GREEN、五否定输入拒绝。旧窗口已消费，修正读器尚无真实执行授权。

本轮证据 (private evidence retained)；修正后新只读窗口清单 (private evidence retained)。当前仍不能推进归档/切换或Engine首启。

## 最新离线推进：imports修正与完整kit18交付通过

用户call_93ee8a19112c4acb9cd5baa834c44ccb接受A后，配置继续imports=[]，仅在固定containerd2.4.1身份、已验证输入为空时兼容dump的唯一默认列表；其他imports/版本/类型和受检字段仍拒绝。guest.py仅新增有限helper并修改daemon_reload比较分支，其他guest AST、模板、builder、权限/资源及PG/Trust/Start未变。

原4762字节dump在原caller完成RED/GREEN；六个重点方法含51个synthetic拒绝子例，Engine176、资格窗口16、自检与Go12通过。未修改官方v2.4.1 LoadConfigWithPlugins的私有正反夹具证明显示默认列表与输入递归imports的区别；仅该实验使用其最低要求的私有Go1.26.6，产品构建仍Go1.26.4，不冒充Linux CLI或guest验收。

原builder一次生成kit0018，canonical bundle1；1076Source/7Templates、1152kit/18bundle叶、20格式、43exit0收据、17逆绑定、13null/4false完整核对。实际新候选最终Go绑定PASS/noSKIP/runtime reads zero，关闭原预构建SKIP。交付1350文件/147目录冻结；源码、交付和整片复审均Approved、C0/I0，整片保留已缓解的私密full-env证据M1。原件0600/0700保持私密，脱敏metadata另存；后续实际env最小化，1170候选文件无私密metadata/敏感值匹配。

Kit manifest f36b78c7a8e9688788a736569a43d76a1a7fbffd304ada0cae0ce13b62008673；bundle manifest 63123fd5ffd1604d6c6fd242e0694899f45b5a1b8f52c9a489aba15a1d91b36e。SourceRoot sha256:b67e9b764fd2a234241e639a7e71280c9ec9a16d5cf3a6714846f712ae4d6e63；TemplatesRoot仍sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce。

本片离线交付时没有进入guest或重试环境。保存态仍为四kit17、七archives、3372/4858两个host stage及已安装guest对象；当时两个guest目录inode未观测，future_execution_blocked=true。随后只读窗口已补齐这两个目录的身份/归属和窗口内稳定性，见上方最新状态；旧封存内容保持。未来保全/切换或Engine/条件首启仍须具体执行器、写入前重验与新的实际范围，不能覆盖现有目标或重放旧actual。

本片入口 (private evidence retained)；整片复审 (private evidence retained)；保全方案 (private evidence retained)。最终封存见completion.json/phase-seal.json。Engine ready→镜像/秘密/PG→资格/安装/同进程首启→真实容量/Create-quota-Start→同Task报告/页面/观测仍需逐段实跑，首条闭环后再定向精简。

## 最新真实推进：目录补齐，完整preflight和安装通过

用户明确授权最小追加清单。新phase `runtime-m1-directory-prerequisite-execution-1025` 核验24964旧封存记录、1996工作树基线和HEAD/index，源码与kit17未变。3372叶旧host stage用RENAME_EXCL保留为engine-preparation-kit17-window-1；四现行kit17和两端七处archive完整核对保持。guest集中核验所有固定父目录、core120%CPU/3GiB/swap0/PIDs1024和统一cgroup2后，唯一缺项/var/lib/switchback已独占创建root:root0700。原清单省略父slice，本次按不变contract.CORE_CGROUP读取/switchback.slice/switchback-core.slice。

新一次原120秒Engine实际运行5.176631334秒exit1。preflight、claim_stage、install_files均成功，daemon_reload两条配置检查均exit0，随后配置比对失败。四个实际安装文件的保存哈希与kit17/source一致；301条命令无error/timeout/truncation。没有执行systemctl daemon-reload、服务Start或网络创建；镜像、新秘密、新PG/迁移、资格240秒窗口/provider未开始。guest failed，host/remote_effects unknown保留；没有失败后guest核查、重试、续跑或remoteStop。新host stage inode144305440/4858叶原字节完整保存。

原调用点回放4762字节containerd dump精确复现；生产受检字段只有imports不同：输入空列表，dump保留默认/etc/containerd/conf.d/*.toml。固定2.4.1上游源码区分默认输出合并与每份输入的递归imports；不能仅据dump列表宣称已加载默认目录。最小离线A候选保持输入imports=[]，在固定版本和安装身份成立时仅兼容这一输出表示；其他imports/字段仍拒绝，加入官方加载器隔离实验、真实回放和拒绝回归，再交付新kit/bundle及复审。随后已接受并完成A离线修订与kit18交付，见上方最新状态；当前未部署该修订。

本轮4919项证据已封存，seal SHA256为f7a47905f3fe3e82f26cb04bb16de1afa5862a64869108b439bb83c2e3d84baf。现场已有安装对象，下一次不能直接重跑旧actual；未来须有明确保留/恢复方案和新具体真实授权。

本轮证据 (private evidence retained)；最小离线修正候选 (private evidence retained)。本轮已按失败即停结束，没有下一窗口授权。

## 历史：完整unit采集完成，安装父目录准备遗漏

用户call_0478421523b54cdca26874fb129f7719明确授权真实准备及一次首启。copy一次exit0，四份kit17/bundle1共2340叶核验通过；五旧对象及2514叶stage no-replace保全，六处archive保持，现两端各七处配对archive。Engine原120秒窗口一次运行5.929956042秒exit1，host/remote_effects/guest unknown，只有preflight；失败即停，没有guest补查、重试或remote Stop。

本次283个unit属性和28模板全部采集完成，293条命令均exit0且无error/timeout/truncation，先前数组解析修正已真实生效。随后安全目录检查报/var/lib/switchback不存在：这是操作准备遗漏父目录；原_mkdir只创建m1-engine叶，要求其父目录预先安全存在。剩余父目录/core cgroup资源/最终aggregate preflight未执行，不能称整体preflight通过，也没有服务安装/启动或后续镜像/秘密/PG/资格首启。

Source和kit17保持原封存，未重建候选。新host stage inode143965924/3372叶完整保留；实际_safe_directory离线回放以缺父目录复现ENOENT，准备0700父目录后通过，unsafe目录仍拒绝。下一步复用当前四份kit17和七archive，先集中收齐剩余固定目录/cgroup前提；只有唯一缺项是该目录才在新授权中exclusive创建root0700，再执行新Engine窗口与原条件首启。不再增加源码修复版本。

本次实跑证据 (private evidence retained)；最小剩余准备清单 (private evidence retained)。本次失败即停授权已消费，下一窗口当前未授权。

## 历史离线推进：kit17完整交付与实际最终绑定通过

生产只在exact catalogue tuple启用Listen/ExecStart保序归并，默认parser、其他show查询和自有unit有效ExecStart检查仍严格唯一。重复数组中的空项拒绝；全部命令项进入原daemon识别，第二/末项dockerd、containerd、shim仍触发既有拒绝。完整unit清单/模板/512上限、listener、权限、资源和PG/Trust/Start保持。

真实调用点RED/GREEN、12项新增重点回归、完整Engine170和资格窗口16通过，self-check通过；Go预检12 PASS，预构建SKIP已由实际新kit17/bundle1 FinalKitBinding PASS/noSKIP/runtime reads zero关闭。39条真实show stdout和2份完整清单已逐字节核对，244条后续ordinary properties仍为显式synthetic。完整离线回放查询283个ordinary/instance并记录28模板，不等于真实窗口38项已解析和4项已遍历模板的观测前缀。

Source4与74条Task1记录冻结，独立源码spec/quality复审C0/I0/M0，Root AST/完整raw provenance/15份原始receipt核验通过。Root把builder临时目录误用于严格安全fixture的首轮失败已保留；改用任务私有0700目录重跑通过，没有放宽生产检查。Root两次审核脚本的schema/alias假设也保留原稿并修正，不涉及Source变更。

原builder一次生成kit17，canonical bundle1完整绑定，SourceRoot为`sha256:bb6b7fdc423c6c39d1b23059ea6c1eecb994beb2a08ff07ca5ab2bce806e2580`、TemplatesRoot仍为`sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce`。1076 Source/7模板、1152完整kit叶/18bundle叶、20格式、43exit0内部收据、17reverse、13null/4false全验。Kit manifest为`1e22ffcdc684146ea0dcb110553fccbe8234c9272ded9577e1961deeb3168ec5`，bundle manifest为`3f317b9ee7edeeb768e7306e75d8febd2d096941cc56fe26947fae12441f494f`。

交付独立评审Approved C0/I0/M1；1240文件/146目录封存、20项计数/重叠/正文负向检查以及Root产物/交付/保存映射审计通过。唯一Minor是早期辅助版本probe只保留规范化文本，旧原始bytes/hash不可恢复；所有主验证原始流完整，后续显式版本原始回执对应相同工具hash，不冒充旧流。Root保留该历史非功能证据局限，未把它写为M0或重跑builder。当前四份kit16、六处archive、2514叶stage及旧证据未读取或修改，本轮没有guest/Engine/PG/provider或候选程序执行。

[接受设计](/path/to/switchback/docs/superpowers/specs/2026-10-24-m1-unit-array-property-design.md)；源码报告 (private evidence retained)；交付报告 (private evidence retained)；整片终审 (private evidence retained)；封存入口 (private evidence retained)；下一具体真实操作候选 (private evidence retained)。当前没有新的环境执行授权；首条体验闭环仍需Engine ready→镜像/秘密/PG→资格/安装/首启→实际容量/Create-quota-Start→同Task报告/页面/观测，之后再定向精简。

## 最新真实窗口：kit16模板修正越过旧错误，多值属性解析失败

用户明确授权“剩余 Engine 部署与条件首启”。本轮没有重复复制或归档，只读核验现有VM Running、四份kit16副本、六处archive及旧stage通过。原120秒Engine窗口一次运行1.600580125秒exit1，host status/remote_effects unknown，guest仅preflight failed；没有安装/服务启动step，也未进入镜像、新秘密、新PG、资格首启或provider。失败后没有guest核查、重试、续跑或remote Stop。

两份完整清单仍为311名称，本次完成38项普通properties并遍历4个模板定义，第39次show dm-event.socket exit0/stdout130/stderr0，含两个Listen行。现有_properties把任何重复key都拒绝，抛出duplicate/malformed systemd property；43条命令全部成功且无error/timeout/truncated，raw wire/hash和cleanup均匹配。真实parser及真实_IO.show调用点离线回放重现；单项/重复标量拒绝control通过。上次模板修正已越过旧故障，但完整preflight和服务首启仍未通过。

新消费host stage inode143593011、2514叶已完整保存。四现行kit16副本、六处archive与生产Source/HEAD/index保持。推荐下一片设计A：仅exact catalogue query保序保存Listen和ExecStart两个数组属性，重复标量、其他查询、完整unit/daemon/listener及权限/资源/PG/Trust/Start保持；ExecStart同类序列化已由上游源码核实，尚无本机重复ExecStart捕获。后续244个普通properties仍未观测，不能当真实全量回放。

本次证据与诊断 (private evidence retained)；[最小数组属性设计](/path/to/switchback/docs/superpowers/specs/2026-10-24-m1-unit-array-property-design.md)。当前仅完成诊断与设计，未改Source或重建kit，也没有新环境执行授权。

## 历史：kit16四副本切换完成，启动曾被自动审批拒绝

用户在具体操作请求后回复“请继续推进”。copy入口一次exit0，四新副本2340叶校验完成，五旧对象（含2425叶stage）以no-replace保全，五处既有archive前后核验不变；现在两端各六处archive。42项离线归档边界检查、17479旧封存记录与1994工作树基线核验通过，产品Source/候选保持原封存版本。

随后Engine启动调用被自动审批审查拒绝：审查认为“请继续推进”不足以覆盖进入guest启动常驻containerd/Engine、固定网络及其持久副作用。命令未创建进程，Engine窗口/claim均未开始。没有绕过、重试或拒绝后guest核查；没有镜像、新秘密、新PG、资格窗口、provider或VMStart。当前四kit16副本与六处archive保留，下一次仅需明确授权剩余Engine与条件首启，不重复复制归档。

本次切换与阻塞证据 (private evidence retained)；仅剩真实启动的具体清单 (private evidence retained)。Engine ready之后仍需镜像/秘密/PG、资格/安装/首启、实际容量/Create-quota-Start及同Task报告/页面/观测闭环；首条体验闭环通过后定向精简。

## 历史离线交付：kit16模板分类修正完成，随后四副本已切换

生产修正只在_unit_catalog：两份完整installed/loaded清单保留，union512与mask coverage不变；exact未实例化模板必须installed-only，独立记录name及来源，不查询/伪造MainPID、ActiveState、ExecStart、Listen，也不加入伪inactive other_units。所有普通unit和实际实例继续原六项properties及Id/ExecStart/daemon/mask/new-unit拒绝。其他guest函数、prepare/contract/资格helper、预算/frame/deadline/权限/资源/PG/Trust/Start保持。

捕获两完整清单311名称/28模板与前4properties及模板exit1经过真实调用点RED/GREEN；其余279 properties明确synthetic，不能当完整真实preflight。focused8、captured replay2、完整Engine158、资格窗口16、自检和所选Go回归通过；原builder一次实际kit0016、canonical bundle001，actual最终Go绑定PASS/noSKIP/runtime reads zero。1076Source/7模板、1152kit叶/18bundle叶、20格式、43exit0收据、17reverse、13null/4false均独立核对。

- Kit manifest：`a174dcdc2acf9915fccd45d7f9c010a3835f41eea326f9710b32ef06b3a1d485`。
- Bundle manifest：`dfea5122c5a292fac1fd1a9581fb0b572925f2c4b6634a7519c591e824a009db`。
- SourceRoot：`sha256:99d2e62b8929ca964be131d44d96166d19e0a7237f4b9a7a71ded754128d1c9f`。
- TemplatesRoot：`sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce`。

Source/交付复审/most-capable整片终审最终均C0/I0/M0。交付初审M1为文案链误写样本297/18；完整四角色补正改为从真实输入推导311/28/283/4/279，11负例拒绝，原1210交付+61Source记录保持，新增66补正记录，当前操作入口已用有效修订清单。没有Source重建、产品suite重跑或环境动作来修本文案。

本片仅读WT保存证据，未进入guest、运行Lima/lsof/Engine/PG/provider/native fixture或候选程序，未触碰现行四副本、2425叶stage或五历史archive。旧host unknown/guest failed保持。新授权窗口须重新验证live前提，no-replace保全五旧对象及五既有archive后独占复制kit16；Engine明确ready后再推进镜像/新秘密/新PG与资格首启，之后才有真实容量/Create-quota-Start和同Task报告/页面/观测闭环。首条体验闭环通过后定向精简。

本片证据 (private evidence retained)；有效交付补正入口 (private evidence retained)；整片终审 (private evidence retained)；新具体真实操作候选（未授权） (private evidence retained)

## 最新真实尝试：kit15清单输出通过，模板运行属性查询失败

用户call_5a0bafd3229744cc89e42ba4c16afe19明确授权本次准备与一次首启。复制入口一次exit0：VM已Running前提通过，五旧对象（含2448叶stage）no-replace完整保留，四历史archive核对不变，四份kit15/bundle1的2340叶校验完成。现在新增unit-catalog/kit15归档后共五个既有archive。

原Engine120秒窗口一次运行1.234733792秒，guest仅preflight failed，host status/remote_effects unknown。两条完整清单exit0且无timeout/truncation：list-unit-files19861 bytes/279行、list-units21630 bytes/194行，union311名称/28模板；这直接验证上一片预算修复在本次实跑生效。逐项properties完成4个普通unit后，对apport-coredump-hook@.service模板执行show返回exit1、stdout0/stderr114，明确提示不是有效运行unit name。原collector混淆安装模板定义与manager运行实例。

没有安装/服务启动step、镜像、新秘密、新PG/迁移、资格窗口/provider、VMStart/Stop/retry或失败后guest核查。本轮消费stage inode141571427/2425叶完整冻结；首次host-only快照复制因相对目标路径被parent checker拒绝，部分副本与失败说明保留，随后仅以绝对WT目标完成证据保存，没有重跑环境。host/guest旧unknown/failed保持。

实际_unit_catalog/_Record的保存packet离线回放：synthetic mask control PASS，捕获模板exit1目标RED。两份清单和前4个properties/模板stderr是真实字节；之后未采集properties明确synthetic，不能作为全量真实properties或未来成功证明。当时提出的设计A保留完整两清单/512上限与所有普通unit/实例的properties/ExecStart检查，模板单独记录定义来源，不伪造运行字段、不吞掉非零，不加载占位实例；该窗口收尾没有改Source，随后本轮kit16离线实施已完成。

本次实跑证据 (private evidence retained)；[模板分类设计（已接受并完成本轮离线交付）](/path/to/switchback/docs/superpowers/specs/2026-10-24-m1-unit-template-catalog-semantics-design.md)

该最小分类修正及整片kit16源码/候选/评审现已完成，下一步为新授权窗口；不能续跑kit15失败窗口。首次实跑仍需Engine ready→镜像/新秘密/新PG→资格/安装/同进程首启→容量/PG/Create-quota-Start→同Task报告/页面/观测闭环；首次真实体验闭环后定向精简。

## 历史：kit15离线交付（随后已执行本次窗口）

本片仅将两个完整exact systemctl清单argv的stdout预算由16384增至262144 bytes；所有stderr、其他stdout和step摘要仍16384。两份完整清单、512名称上限、逐项properties/ExecStart、mask/foreign/new-unit、raw1MiB/累计64MiB、进程身份与原deadline/权限/PG/Trust/Start门禁保持。新增回归收在现有测试文件，没有扩大生产源码叶白名单。

有效RED/GREEN和捕获前缀control/+1 synthetic回放完成；focused11、完整Engine150、资格窗口16和self-check通过。原builder一次生成kit0015，canonical bundle001；1076Source/7模板、1152完整kit叶、18bundle叶、20格式、43exit0构建收据、17逆输入、13null/4false均由实际产物与Root独立核对。实际新kit/bundle最终Go绑定PASS、noSKIP、runtime reads zero；三份独立Source/交付/整片评审均C0/I0/M0。

- Kit manifest：`197ea1f9460f7c53ac2f516f677082208f593fbd547a7e71b7d5219474e9ae1a`。
- Bundle manifest：`4d9fc844a952efbfeee9c5b737d432bac6fbbea05722f18cec3285a10d1e9749`。
- SourceRoot：`sha256:ed59bdfc85fa3e9e750a28f822622d9dc7d45a285503031bff05353700520081`。
- TemplatesRoot：`sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce`。

本次只有四个允许Source文件变化，不启动guest/Engine/PG/provider，不读取live四副本/stage/archive，不生成秘密或执行候选binary。最新保存事实仍是四份kit14/bundle1和2448叶unknown stage；四历史archive完整保留。新具体清单要求下一授权窗口重新核验、no-replace保全五旧对象，再独占切换四新副本。真实完整catalogue大小仍未知，不能从16KiB前缀或synthetic清单保证下次成功。

本片证据 (private evidence retained)；整片终审 (private evidence retained)；下一次具体操作候选（未授权） (private evidence retained)

下一步是新授权的kit15真实准备与单次首启；Engine明确ready后才进入镜像/新秘密/新PG、资格→安装→同进程首启，再验证实际容量/PG/Create→quota→Start→Worker任务→正式报告→页面接受→同Task观测闭环，首次真实体验闭环后才定向精简。本次离线通过不代表上述运行阶段完成。

## 最新真实尝试：kit14完整切换后unit catalogue输出超限

用户在上一条完整封存操作清单后要求“继续推进吧”；Root按该具体清单执行新的单次窗口，未复用旧离线或失败grant。新copy入口exit0：重新核验VM已Running4CPU/10GiB、Source/模板、四固定副本、1954叶stage和三旧archive；五旧对象no-replace完整保留后，四份新kit14/bundle1独占复制并完成2340新leaf与全部旧归档检查。

原Engine actual一次120秒窗口实际1.729684417秒结束。preflight形成两份各128进程的完整process/FD inventory、store扫描及listener inventory，随后固定 `/usr/bin/systemctl list-unit-files --all --type=service --type=socket --no-legend --no-pager --plain` 的stdout超过通用16KiB预算。执行器保留16384-byte前缀（230个完整换行）、truncated=true，终止自有命令进程组，exit-9，无timeout/stderr。完整输出大小未知，前缀不能当完整catalogue。

host status/remote_effects与guest status均保留unknown；只有preflight一个step，known/unknown effect数组空不提升为全局无副作用。未进入安装/服务启动步骤，没有镜像、新key/secret、新PG/迁移、资格窗口或provider。没有重试、续跑、失败后guest核查或远程Stop。本次新host stage原inode141118002、2448文件完整冻结，不能复用或清理。

实际 `_bounded_run` 的离线fake child/selector/signal复现：真实16KiB前缀control通过，前缀+1 synthetic byte触发同一错误。只证明buffer边界，不是完整catalogue回放；本轮没有观察到RS实际状态迁移，不能宣称该分支已由live对比验证。R/S修复的此前源码/真实pair离线回归和完整新kit绑定仍保持。

本轮证据 (private evidence retained)；[unit清单stdout最小修复设计（本轮已接受并离线完成）](/path/to/switchback/docs/superpowers/specs/2026-10-24-m1-unit-catalog-output-budget-design.md)

## 最新真实位置与下一步

| 项目 | 当前保存事实 |
| --- | --- |
| 四固定副本 | host/guest均已验证为kit14/bundle1；仅截至本次切换时点 |
| 新kit manifest | `0c1a7bea7a8c3e4b915b602bb9249ebe72da7e8a285a0e2d2afd2c298b359587` |
| 新bundle manifest | `3310f178be6bbcbd1677c3a68b84401884f28a499470baed8de713176f503dbd` |
| 新归档 | process-state/kit14名下保留四份旧kit13副本与1954叶stage |
| 更早归档 | quota/kit11、volume/kit12、empty-plugin/kit13三处均保持；现在连同新归档共四处 |
| 最新已消费stage | root inode141118002，2448文件；host/guest unknown |
| 已明确阻塞 | 完整unit清单超出16KiB；不是timeout或已证明的systemctl业务错误 |
| 最新离线交付 | kit15/bundle1已完整交付；本轮未操作现行kit14或stage，下一真实窗口待授权 |

本轮已完成该设计的整片离线修复与完整kit15/bundle1；下一步按新具体清单保留四现行kit14副本、2448叶stage及四处archive，执行新授权窗口。旧claim/窗口不能续跑；之后仍需Engine ready→镜像/新秘密/新PG→资格/安装/同进程首启→实际容量/PG/Create-quota-Start→同Task报告/页面/观测闭环，首条体验闭环后再精简。当前没有可证明的成功百分比或工期。

## 历史：已完成的R/S离线切片

### R/S切片源码与验证

生产 `_IO.processes` 只修正原比较点：PID、start tick、全部flags一致时允许R/S切换；其他状态迁移、same-state旧行为、parser/before输出、FD/owner/socket/listener/cgroup/exe/进程表复查和限时保持。Go仅增加本phase新bundle发现路径。没有修改权限、资源或PG/Trust/Start门禁。

真实S→R的stat/cgroup/cmdline字节已与1933-frame封存packet匹配，通过实际collector调用完成RED/GREEN，保留R→S、identity/flags变化和其他state拒绝、PID1/kernel-thread例外回归。exe/空FD为synthetic隔离输入，不是完整inventory或guest成功。Engine139项、资格窗口16项、自检通过；实际新kit/bundle Go最终绑定PASS、noSKIP、runtime reads zero。

原builder一次实际生成kit0014，canonical bundle001及verify通过。Task2与Root独立完整审计覆盖1076Source+7Templates、1151covered+manifest、20formats、43exit0构建收据、17reverse inputs、13runtime null、4authority false。Task1、Task2和most-capable整片独立评审均C0/I0/M0；全部⚠边界由Root裁决，不把运行资格混入离线完成。

本片交付证据 (private evidence retained)；终审 (private evidence retained)

### 当时的候选与真实状态

| 项目 | 当前事实 |
| --- | --- |
| 最新离线候选 | kit-0014 / 本phase bundle-0001，未复制部署 |
| Kit manifest SHA256 | `0c1a7bea7a8c3e4b915b602bb9249ebe72da7e8a285a0e2d2afd2c298b359587` |
| Bundle manifest SHA256 | `3310f178be6bbcbd1677c3a68b84401884f28a499470baed8de713176f503dbd` |
| SourceRoot | `sha256:73e511022d0765d925b41effde1b37fdac33d27d3b7b3997d84085b5ed55c49f` |
| TemplatesRoot | `sha256:5280141c37f7152f1c00194955dba8614daec2514a681d5af826696bc89e89ce` |
| 现行四固定副本的最近保存事实 | host/guest kit13/bundle1，四份复制已完整通过2340新leaf检查；本片未live重验 |
| 最新真实Engine尝试 | kit13一次窗口，host1.700965秒，guest仅preflight失败；host status/remote_effects仍unknown |
| 已消费host stage | 原inode140965207，1954叶完整冻结；不能清理或复用claim/window |
| 需保留的旧对象 | 当前四份kit13/bundle1和1954叶stage；另外三个更早archive保持 |
| 执行状态 | runtime资格/容量ready/Start/provider没有由本片取得；原门禁保持 |

### 上一kit13失败与修复覆盖

kit13曾完成剩余复制恢复：宿主已归档三对象保持，guest两个旧副本no-replace完整归档，再独占复制四份kit13/bundle1；五旧对象和两个当时更早archive都已保全。随后原Engine actual在preflight报 `process identity drift during FD enumeration`，未进入后续安装/启动step；镜像、新秘密、新PG/迁移、资格240秒窗口/provider均未开始。host unknown与guest failed原样保留，没有失败后guest readback/重试/续跑/Stop。

packet frame1918/1931的PID1348、start_tick544、flags4194624完全相同，仅state从S到R；旧代码整体dict比较触发误判。本片源码与kit14已修复这个比较错误，但新候选尚未经历真实Engine窗口，其他未执行阶段仍可能暴露问题。

真实失败证据 (private evidence retained)；[已接受修复设计](/path/to/switchback/docs/superpowers/specs/2026-10-24-m1-process-state-identity-design.md)

### R/S交付时的后续清单（已被上方kit14结果更新）

1. 对完整新候选取得一次具体真实授权，重新核验所有当前状态；将四现行kit13副本和1954叶stage完整no-replace归档，保留三个既有archive，独占复制四份kit14/bundle1。不重用旧copy入口/claim或失败stage。
2. 执行一次原120秒Engine窗口。只有明确ready后，继续候选镜像、新key/secret、新私有PG/迁移、既定bind与有限双能力采集，以及一次原240秒资格→安装→同进程首启；barrier300秒与PG/Trust/Start门禁保持。失败/unknown即停，不重试或远程Stop。
3. 首启通过后，验证新鲜签名容量、PG原子预留、Create→真实quota→单次Start→Worker任务→正式报告→页面接受→同Task观测关联。
4. 第一条真实体验闭环通过后，沿实际路径合并重复配置/转换/诊断入口，减少人工交接并定向精简。

下一次具体操作清单（未授权执行） (private evidence retained)

本轮8425旧sealed records、1989 baseline、允许四源码、ledger prefix、受保护文件和HEAD/index均核对保持。早期测试setup与离线文档/冻结失败完整保留，修正后对应结果通过，未重跑builder或历史native fixtures。

继续复用现有卷/虚拟盘和 `m1_shared_volume_capacity_v1`，不要求外置盘、不改主盘分区、不迁移存储；真实hard quota、80%记账和PG/Start门禁保留。共享APFS/metadata耗尽的可用性风险仍按已接受保证处理。当前没有可证明的成功百分比或工期；离线完成不等于实跑成功。
