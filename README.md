# QiDocker

QiStore 为 Synology DSM 开发的独立 Docker Engine 套件。在 DSM 主菜单打开 QiDocker，管理窗口沿用经典 DSM 桌面的七栏布局。管理请求经 DSM 会话认证，套件自身只使用 Unix Socket。

**当前为 DSM 6.2 x86_64 开发构建。实际 DSM 安装和容器运行需要在隔离设备上验收。** 验收范围和当前限制见[兼容性与验证记录](docs/verification.md)。

## 从源码构建

在 Linux x86_64 构建环境安装 Go 1.24.13、GCC、Git、make、pkg-config、libseccomp 开发文件、Python 3、GNU tar 和 binutils，然后运行：

```sh
./scripts/build-engine.sh
./scripts/package.sh
(cd dist && sha256sum -c ./*.sha256)
```

构建脚本核对各上游发行标签的 commit。二进制和上游许可随 SPK 打包。测试和构建日志位于 [GitHub Actions](https://github.com/QiStore/QiDocker/actions)。通过 DSM 验收后，推送对应的版本标签（例如 `v0.1.0-0002`），工作流会重新构建、运行 Linux 验收并发布通过校验的 SPK 与日志。

## 安装、运行和卸载

1. 在隔离的 DSM 6.2 测试机上用套件中心的「手动安装」选择 SPK。升级时用同一存储卷安装更高版本的 SPK。
2. 启动 QiDocker，并通过 DSM 主菜单打开管理窗口。先查看「总览」的引擎状态，再进入「设置」测试代理。
3. 停止或卸载前检查正在运行的容器。卸载默认保留配置和数据。重新安装到原卷会重新打开原来的 QiDocker 数据目录。
4. 以 SSH 管理员身份运行命令时使用独立封装命令：

```sh
sudo /var/packages/QiDocker/target/bin/qidocker-cli pull hello-world:latest
sudo /var/packages/QiDocker/target/bin/qidocker-cli run --rm --network none hello-world:latest
sudo /var/packages/QiDocker/target/bin/qidocker-cli compose version
```

容器和映像存放在套件所在卷的 `@QiDocker/data`。配置和引擎日志存放在同一卷的 `@QiDocker`。引擎使用独立 Socket，套件也使用独立 Docker CLI 配置。

## 代理

默认跟随 DSM。当前适配器按环境变量格式读取 `/etc/proxy.conf` 和 `/etc/environment` 中的 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`，支持 URL 内认证及 `proxy_username`、`proxy_password`。不同 DSM 构建写入系统代理的格式和路径需要实机核对。

「设置」可切换到自定义代理或直连。自定义代理填写完整 URL，例如 `http://proxy.example:3128`，并在账号和密码字段填写认证。代理协议指连接代理本身使用的协议，HTTPS 目标也可以经 HTTP 代理 CONNECT 转发。「测试已保存设置」会检查代理地址和对 Docker Hub Registry、Auth 的访问。诊断会主动测试 Docker Hub 的代理路径。

当代理配置发生变化，后台会在引擎空闲时尝试应用。正在运行容器时会保留待应用状态，可在确认重启影响后选择立即应用。代理密码保存在权限为 `0600` 的配置中，设置页不会回填密码。

## 配置备份和恢复

以下只备份代理和注册表配置。备份内含敏感数据，请按密码文件保管。恢复会停止并重启 QiDocker。将示例目录换成你的备份目录。

```sh
sudo /var/packages/QiDocker/scripts/config-backup backup /volume1/QiDocker-backup
sudo /var/packages/QiDocker/scripts/config-backup restore /volume1/QiDocker-backup
```

## English

QiDocker builds an isolated Docker Engine bundle for Synology DSM and registers an application in the DSM desktop. The x86_64 DSM 6.2 package is a development build. See [verification status](docs/verification.md) for tested components and outstanding NAS acceptance criteria.
