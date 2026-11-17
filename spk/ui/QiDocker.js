/* global Ext, SYNO */
(function () {
    "use strict";

    const base = "/webman/3rdparty/QiDocker/";
    const assetVersion = "0006";
    const pages = ["总览", "容器", "注册表", "映像", "网络", "日志", "设置"];

    function node(tag, parent, text) {
        const element = document.createElement(tag);
        if (text != null) element.textContent = text;
        if (parent) parent.appendChild(element);
        return element;
    }

    async function api(action, query, payload, format) {
        const params = new URLSearchParams(Object.assign({action}, query));
        const options = {credentials: "same-origin", headers: {"X-QiDocker-Request": "1"}};
        const token = window.SYNO && SYNO.SDS && SYNO.SDS.Session && SYNO.SDS.Session.SynoToken;
        if (typeof token === "string" && token) options.headers["X-SYNO-TOKEN"] = token;
        if (payload !== undefined) {
            options.method = "POST";
            options.body = payload instanceof Blob ? payload : JSON.stringify(payload);
        }
        const response = await fetch(base + "control.cgi?" + params, options);
        if (!response.ok) throw new Error((await response.text()).slice(0, 500) || "HTTP " + response.status);
        if (format === "response") return response;
        if (format === "text") return response.text();
        if (response.status === 204) return null;
        const text = await response.text();
        return text ? JSON.parse(text) : null;
    }

    function button(parent, title, action, error) {
        const element = node("button", parent, title);
        element.type = "button";
        element.onclick = async () => {
            element.disabled = true;
            try { await action(); } catch (issue) { error(issue.message); }
            finally { element.disabled = false; }
        };
        return element;
    }

    function fields(parent, definition, onChange) {
        const form = node("div", parent);
        form.className = "qd-form";
        const inputs = {};
        definition.forEach(([key, title, value, type]) => {
            const label = node("label", form);
            node("span", label, title);
            let input;
            if (Array.isArray(type)) {
                input = node("select", label);
                type.forEach(([id, name]) => { node("option", input, name).value = id; });
            } else {
                input = node(type === "textarea" ? "textarea" : "input", label);
                if (type !== "textarea") input.type = type || "text";
            }
            if (type === "password") input.autocomplete = "new-password";
            input.value = value || "";
            if (onChange) input.onchange = onChange;
            inputs[key] = input;
        });
        return () => Object.fromEntries(Object.entries(inputs).map(([key, input]) => [key, input.value]));
    }

    function table(parent, headings, rows, renderActions) {
        const result = node("table", parent);
        const header = node("tr", node("thead", result));
        headings.forEach(heading => node("th", header, heading));
        if (renderActions) node("th", header, "操作");
        const body = node("tbody", result);
        rows.forEach(([values, object]) => {
            const row = node("tr", body);
            values.forEach(value => node("td", row, value));
            if (renderActions) renderActions(node("td", row), object);
        });
        if (!rows.length) node("p", parent, "暂无记录");
    }

    function tab(root) {
        const nav = node("nav", root);
        const content = node("section", root);
        const icons = ["▣", "▤", "⌕", "▧", "⌂", "☷", "⚙"];
        let generation = 0;
        let timer;
        const change = index => {
            clearInterval(timer);
            const revision = ++generation;
            content.replaceChildren();
            [...nav.children].forEach((child, i) => child.classList.toggle("active", i === index));
            const feedback = node("div", content);
            feedback.className = "qd-feedback";
            const body = node("div", content);
            const error = message => { feedback.textContent = message; };
            const reload = () => change(index);
            const btn = (where, label, handler) => button(where, label, handler, error);
            const details = async (action, id, text) => {
                const result = await api(action, {id});
                const out = node("pre", body, text + "\n" + JSON.stringify(result, null, 2));
                btn(body, "关闭详情", () => out.remove());
            };
            const refresh = callback => {
                callback().catch(issue => error(issue.message));
                timer = setInterval(() => callback().catch(issue => error(issue.message)), 3000);
            };
            const remove = async (action, query) => {
                if (window.confirm("确认删除？")) { await api(action, query, {}); reload(); }
            };

            async function overview() {
                const [status, host] = await Promise.all([api("status"), api("host")]);
                const containers = status.online ? await api("containers") : [];
                if (generation !== revision) return;
                body.replaceChildren();
                const cards = node("div", body);
                cards.className = "qd-cards";
                const gb = value => (value / 1073741824).toFixed(2) + " GB";
                [["CPU 使用率", host.cpu.toFixed(1) + "%", host.cpu, "cpu"],
                 ["内存使用率", gb(host.memory_used) + " / " + gb(host.memory_total),
                     host.memory_total ? host.memory_used * 100 / host.memory_total : 0, "ram"]]
                    .forEach(([title, value, used, kind]) => {
                        const card = node("div", cards);
                        card.className = "qd-card " + kind;
                        node("strong", card, title);
                        node("b", card, value);
                        const meter = node("progress", card);
                        meter.max = 100;
                        meter.value = used;
                    });
                node("p", body, "Docker Engine " + (status.online ? "运行中" : "离线") + " · 容器 " + containers.length);
                node("h3", body, "运行容器");
                const running = containers.filter(item => item.State === "running");
                table(body, ["名称", "映像", "状态"], running.map(item => [[item.Names.join(", "), item.Image, item.Status]]));
                if (status.proxy_pending) node("p", body, "代理有待应用的更改");
            }

            async function containers() {
                btn(body, "创建容器", () => {
                    const pane = node("div", body);
                    const read = fields(pane, [
                        ["name","容器名"], ["image","映像"], ["command","命令（JSON 数组）", "", "textarea"],
                        ["env","环境变量（每行 KEY=VALUE）","","textarea"],
                        ["binds","卷挂载（每行来源:目标[:ro]）","","textarea"],
                        ["ports","端口映射（每行 主机端口:容器端口/protocol）","","textarea"],
                        ["network","网络","none"]
                    ]);
                    btn(pane, "创建", async () => {
                        const v = read();
                        const lines = raw => raw.split("\n").map(value => value.trim()).filter(Boolean);
                        const bindings = {}, exposed = {};
                        lines(v.ports).forEach(line => {
                            const [host, port] = line.split(":");
                            if (!/^\d+$/.test(host) || !/^\d+\/(tcp|udp)$/.test(port || "")) {
                                throw new Error("端口格式示例为 8080:80/tcp");
                            }
                            exposed[port] = {};
                            (bindings[port] ||= []).push({HostPort: host});
                        });
                        await api("create", {name: v.name}, {
                            Image: v.image, Cmd: v.command ? JSON.parse(v.command) : undefined, Env: lines(v.env),
                            ExposedPorts: exposed, HostConfig: {Binds: lines(v.binds), PortBindings: bindings, NetworkMode: v.network}
                        });
                        reload();
                    });
                    btn(pane, "取消", () => pane.remove());
                });
                const rows = await api("containers");
                table(body, ["名称","映像","状态"], rows.map(item => [[item.Names.join(", "), item.Image, item.Status], item]), (cell, item) => {
                    const id = item.Id;
                    btn(cell,"详情", () => details("container",id,"容器详情"));
                    btn(cell,"资源", () => details("stats",id,"容器资源统计"));
                    [["启动","start"],["停止","stop"],["重启","restart"],["暂停","pause"],["恢复","unpause"]].forEach(([name, action]) => {
                        btn(cell,name,async () => { await api(action,{id},{}); reload(); });
                    });
                    btn(cell,"日志",() => {
                        const output = node("pre",body);
                        clearInterval(timer);
                        refresh(async () => { output.textContent = await api("logs",{id},undefined,"text"); });
                    });
                    btn(cell,"执行命令",() => {
                        const pane = node("div",body);
                        const read = fields(pane,[["command","容器内 Shell 命令"]]);
                        const output = node("pre",pane);
                        btn(pane,"执行",async () => { output.textContent = await api("exec",{id},read(),"text"); });
                        btn(pane,"关闭",() => pane.remove());
                    });
                    btn(cell,"删除", () => remove("remove",{id}));
                });
            }

            async function pull(image) {
                const area = node("div", body);
                node("strong", area, image);
                const layers = new Map();
                const output = node("p", area, "正在请求映像");
                const res = await api("pull", {image}, {}, "response");
                const reader = res.body.getReader();
                const decoder = new TextDecoder();
                let tail = "", complete = false;
                const consume = line => {
                    if (!line.trim()) return;
                    const message = JSON.parse(line);
                    if (message.error) throw new Error(message.error);
                    output.textContent = message.status || "";
                    if (!message.id) {
                        if (/Status:/.test(message.status || "")) complete = true;
                        return;
                    }
                    if (!layers.has(message.id)) {
                        const row = node("div", area);
                        node("span", row, message.id);
                        layers.set(message.id, [node("progress", row), node("span", row)]);
                    }
                    const [meter,label] = layers.get(message.id);
                    const progress = message.progressDetail || {};
                    if (progress.total) { meter.max = progress.total; meter.value = progress.current || 0; }
                    else if (/complete|already exists/i.test(message.status || "")) { meter.max = 1; meter.value = 1; }
                    else meter.removeAttribute("value");
                    label.textContent = " " + (message.status || "");
                };
                try {
                    for (;;) {
                        const {value,done} = await reader.read();
                        if (done) break;
                        tail += decoder.decode(value,{stream:true});
                        const lines = tail.split("\n");
                        tail = lines.pop();
                        lines.forEach(consume);
                    }
                    consume(tail + decoder.decode());
                    if (!complete) throw new Error("映像下载流已中断");
                } finally { await reader.cancel().catch(() => {}); }
            }

            async function images() {
                const read = fields(body,[["image","映像名称","hello-world:latest"]]);
                btn(body,"拉取", () => pull(read().image));
                const file = node("input",body);
                file.type = "file";
                btn(body,"导入",async () => {
                    if (!file.files[0]) throw new Error("请选择映像归档");
                    const result = await api("import",{},file.files[0],"text");
                    node("pre",body,result);
                });
                btn(body,"刷新", reload);
                const rows = await api("images");
                table(body, ["标签","大小"], rows.map(item => [[(item.RepoTags || []).join(", "), (item.Size / 1048576).toFixed(1) + " MB"],item]), (cell,item) => {
                    const id = item.Id;
                    btn(cell,"详情", () => details("image",id,"映像详情"));
                    btn(cell,"添加标签", () => {
                        const row = node("div",body);
                        const values = fields(row,[["repo","仓库"],["tag","标签","latest"]]);
                        btn(row,"保存",async () => { await api("tag",{id,...values()},{}); reload(); });
                    });
                    btn(cell,"导出", async () => {
                        const res = await api("export",{id},undefined,"response");
                        const link = node("a",body);
                        link.href = URL.createObjectURL(await res.blob());
                        link.download = "qidocker-image.tar";
                        link.click();
                        setTimeout(() => { URL.revokeObjectURL(link.href); link.remove(); },10000);
                    });
                    btn(cell,"删除", () => remove("remove-image",{id}));
                });
            }

            async function registries() {
                const read = fields(body,[["server","注册表","docker.io"],["username","账号"],["password","密码","","password"]]);
                btn(body,"登录并保存",async () => { await api("registry-login",{},read()); reload(); });
                const search = fields(body,[["term","映像名称"]]);
                btn(body,"搜索",async () => {
                    const rows = await api("search",search());
                    table(body,["名称","简介","星标"],rows.map(item => [[item.name,item.description,item.star_count],item]),
                        (cell,item) => btn(cell,"拉取",() => pull(item.name)));
                });
                btn(body,"拉取指定映像",() => pull(search().term));
                const rows = await api("registries");
                table(body,["注册表","账号"],rows.map(item => [[item.server,item.username],item]),
                    (cell,item) => btn(cell,"移除凭据",async () => { await api("registry-logout",{},{server:item.server}); reload(); }));
            }

            async function networks() {
                const read = fields(body,[["Name","网络名称"],["Driver","驱动","bridge"]]);
                btn(body,"创建网络",async () => { await api("create-network",{},read()); reload(); });
                const rows = await api("networks");
                table(body,["名称","驱动","范围"],rows.map(item => [[item.Name,item.Driver,item.Scope],item]),(cell,item) => {
                    btn(cell,"详情",() => details("network",item.Id,"网络详情"));
                    btn(cell,"删除",() => remove("remove-network",{id:item.Id}));
                });
            }

            async function settings() {
                const state = await api("status");
                const proxy = state.proxy || {};
                const readMode = fields(body, [
                    ["mode","代理模式",proxy.mode || "dsm",[["dsm","跟随 DSM"],["custom","自定义"],["direct","直连"]]]
                ], renderMode);
                const modePanel = node("div", body);
                let readCustom;
                function renderMode() {
                    modePanel.replaceChildren();
                    readCustom = null;
                    switch (readMode().mode) {
                    case "dsm":
                        node("p", modePanel, "代理配置由 DSM 控制面板 → 网络 → 常规统一管理。");
                        if (proxy.mode === "dsm" && !state.proxy_configured) {
                            node("p", modePanel, "QiDocker 尚未读取到 DSM 代理地址。");
                        }
                        break;
                    case "custom": {
                        const saved = proxy.mode === "custom" ? proxy : {};
                        readCustom = fields(modePanel, [
                            ["http","HTTP 代理 URL",saved.http], ["https","HTTPS 代理 URL",saved.https],
                            ["username","代理账号",saved.username], ["password","代理密码（留空保留原密码）","","password"],
                            ["no_proxy","不使用代理的地址",saved.no_proxy]
                        ]);
                        break;
                    }
                    case "direct":
                        node("p", modePanel, "直接连接 Docker Hub。");
                        break;
                    }
                }
                renderMode();
                node("p",body,state.proxy_pending ? "代理修改将在没有运行中容器时自动应用。" : "重启引擎可能中断正在运行的容器。");
                btn(body,"保存",async () => {
                    const settings = readMode();
                    if (readCustom) Object.assign(settings, readCustom());
                    await api("proxy-save",{},settings);
                    reload();
                });
                btn(body,"测试已保存设置",async () => {
                    const rows = await api("proxy-test",{},{});
                    table(body,["检查项","结果","详情"],rows.map(item => [[item.name,item.ok ? "通过" : "失败",item.detail]]));
                });
                btn(body,"立即重启并应用",async () => {
                    if (window.confirm("重启 Docker Engine 可能中断正在运行的容器。确定重启？")) {
                        await api("proxy-apply",{},{}); error("已请求重启");
                    }
                });
            }
            const handlers = [
                () => refresh(overview), containers, registries, images, networks,
                () => { const log = node("pre",body); refresh(async () => { log.textContent = await api("engine-log",{},undefined,"text"); }); },
                settings
            ];
            Promise.resolve(handlers[index]()).catch(issue => error(issue.message));
        };
        pages.forEach((name, index) => {
            const item = node("button",nav);
            node("span",item,icons[index]);
            node("b",item,name);
            item.onclick = () => change(index);
        });
        change(0);
        return () => { ++generation; clearInterval(timer); };
    }

    Ext.ns("SYNO.SDS.QiDocker");
    const cls = SYNO.SDS.QiDocker;
    cls.Instance = Ext.extend(SYNO.SDS.AppInstance, {
        appWindowName: "SYNO.SDS.QiDocker.MainWindow",
        constructor: function () {
            cls.Instance.superclass.constructor.apply(this, arguments);
        }
    });
    cls.MainWindow = Ext.extend(SYNO.SDS.AppWindow, {
        constructor: function (args) {
            this.appInstance = args.appInstance;
            const config = Ext.apply({
                title: "QiDocker", width: 1060, height: 570, minWidth: 730, minHeight: 430
            }, args);
            config.icon = base + "icon_0006_24.png";
            cls.MainWindow.superclass.constructor.call(this, config);
        },
        afterRender: function () {
            cls.MainWindow.superclass.afterRender.apply(this, arguments);
            this.mountQiDocker();
        },
        onOpen: function () {
            const result = cls.MainWindow.superclass.onOpen.apply(this, arguments);
            this.mountQiDocker();
            return result;
        },
        mountQiDocker: function () {
            const body = this.body && this.body.dom;
            if (!body || (this.qiDockerRoot && body.contains(this.qiDockerRoot))) return;
            if (this.disposeQiDocker) this.disposeQiDocker();
            const root = node("div", body);
            root.className = "qd-desktop";
            this.qiDockerRoot = root;
            if (!document.querySelector('link[data-qidocker="' + assetVersion + '"]')) {
                const css = node("link", document.head);
                css.rel = "stylesheet";
                css.href = base + "style.css?v=" + assetVersion;
                css.setAttribute("data-qidocker", assetVersion);
            }
            try {
                this.disposeQiDocker = tab(root);
            } catch (issue) {
                root.innerHTML = "";
                node("p", root, "QiDocker 界面加载失败。请关闭后重开窗口。");
                node("pre", root, issue.message);
            }
        },
        onDestroy: function () {
            if (this.disposeQiDocker) this.disposeQiDocker();
            this.disposeQiDocker = null;
            cls.MainWindow.superclass.onDestroy.apply(this, arguments);
        },
        onClose: function () {
            cls.MainWindow.superclass.onClose.apply(this, arguments);
            this.doClose();
            return true;
        }
    });
})();
