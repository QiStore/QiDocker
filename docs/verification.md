# 兼容性与验证记录

| 项目 | 状态 |
| --- | --- |
| x86_64 Linux 上游源码编译 | dockerd、Docker CLI、Containerd、runc、Compose、tini 已编译 |
| DSM 6.2 x86_64 SPK | 开发打包目标，DSM 套件中心验收待测 |
| Linux 隔离引擎启动 | 当前执行环境拒绝创建 Unix Socket，启动测试被阻断 |
| 真实 hello-world | 待在具备容器运行权限的 VM 执行 |
| DSM 主菜单、管理员会话及 CGI | 已实现，DSM 实机验证待测 |
| DSM 系统代理 | 配置解析、凭据编码和变更检测可本地测试。控制面板实际配置路径和格式待核对 |
| 自定义代理、注册表、导入导出 | 已接入 Engine API，端到端待测 |
| 升级、卸载、保留和恢复 | 已提供脚本，需要在 DSM 套件中心按三种保留选项分别测试 |
| DSM 6.0/6.1 | 待评估 |
| DSM 7.x，含 7.2+ | 第三方 root 套件受 DSM 签名限制，当前开发包的 rootful 运行方式无法直接用于标准 DSM 7 |
| ARM64 | 打包脚本含目标架构映射，尚无实际构建及 DSM 运行记录 |

当前引擎为避免与官方 Docker 改动同一套主机防火墙规则，禁用了其自动设置 iptables、转发、伪装与默认网桥。隔离验收使用 `--network none`。桥接网络出网、完整端口发布与官方套件共存，还需要验证独立网络隔离方案。当前容器执行页面支持逐条执行 Shell 命令，交互式终端尚待实现。Docker Hub 搜索走 Docker Engine，部分私有 Registry 仅提供直接拉取接口。

在 Linux 隔离虚拟机上运行 `sudo ./scripts/smoke-linux.sh <SPK> <引擎日志>`，该脚本测试引擎启动、真实映像拉取、运行、删除、导入导出和引擎重启后的映像保留。

DSM 验收顺序为安装、引擎启动、桌面窗口打开、容器生命周期、镜像操作、私有注册表认证、DSM 代理同步、自定义代理及认证、代理修改和重启持久化、升级、卸载后重装和恢复数据。操作需在独立 DSM 实例上完成。

[Synology DSM Package Developer Guide](https://help.synology.com/developer-guide/getting_started/system_requirement.html)
