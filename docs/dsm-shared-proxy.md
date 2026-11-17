# DSM 与 QiDocker 共用代理入口

在同一端口接收明文 HTTP 代理和 TLS 代理。DSM 填写域名、端口及现有认证，QiDocker 使用「跟随 DSM → 自动识别」。纯 HTTP 仍由现有认证代理提供，TLS 仍由现有 TLS 入口终止。

以下示例适用于已确认监听 `17895`（HTTP）和 `17896`（TLS）的 Rocky 服务器。把这段加入 Nginx 的 `stream` 上下文，在新增监听前确认 `17897` 空闲，且当前 Nginx 有 `stream_ssl_preread` 模块。

```nginx
map $ssl_preread_protocol $qidocker_proxy_backend {
    ""      127.0.0.1:17895;
    default 127.0.0.1:17896;
}

server {
    listen 17897;
    ssl_preread on;
    proxy_pass $qidocker_proxy_backend;
}
```

用当前 Nginx 服务的二进制和配置运行语法检查，确认通过后重载。按现有代理端口的访问范围开放 17897。分别验证同一端口的 HTTP 代理连接和 HTTPS 代理证书、认证以及 Docker Hub。此时才将 DSM 控制面板的代理改为 `agentdl.qiqi520.cn` 和 `17897`，沿用原账号认证。如果 QiDocker 留有旧的专用入口，把它清空并保存自动识别选项。全新安装默认自动识别。
