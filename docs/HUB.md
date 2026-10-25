# hub 部署

hub 必须在一台**真正常开**的机器上。Grok Bot 的云电脑会在空闲时停机（Tailscale 后台能看到它的节点 last seen 掉线），不适合；一台几块钱的 VPS 或一台不合盖的 Mac mini 都行。hub 不需要 root、不需要装 Tailscale、不需要开防火墙入站：tsnet 在进程内监听，流量经 Tailscale 的 DERP 中继或直连 UDP 进出。

## 准备

1. Tailscale 后台 → Settings → Keys → Generate auth key，勾 **Reusable**。写入 `$KHOOMEI_HOME/authkey`。
2. 决定 owner handle（小写短名，如 `junyi`）。hub 的节点名会是 `khoomei-<handle>`。
3. 每个 caller 的节点名：机器已在 tailnet 的（Muse）用后台 Machines 里显示的名字；用 `--transport tsnet` 的（Grok）是 `khoomei-<handle>-<agent>`。
4. Grok 的 webhook URL 和 key（见 [PROMPTS.md](PROMPTS.md) 里 Grok 那段），写入 `wake-url` / `wake-secret`。

## Debian / Ubuntu + systemd

```sh
useradd --system --home-dir /var/lib/khoomei --create-home --shell /usr/sbin/nologin khoomei
curl -fsSLo /tmp/khoomei https://github.com/zjywill/khoomei/releases/latest/download/khoomei-linux-amd64
curl -fsSLo /tmp/SHA256SUMS https://github.com/zjywill/khoomei/releases/latest/download/SHA256SUMS
(cd /tmp && grep khoomei-linux-amd64 SHA256SUMS | sed 's/khoomei-linux-amd64/khoomei/' | sha256sum -c -)
install -m 755 /tmp/khoomei /usr/local/bin/khoomei

cat > /etc/systemd/system/khoomei.service <<'EOF'
[Unit]
Description=khoomei hub (embedded tailnet node)
After=network-online.target
Wants=network-online.target

[Service]
User=khoomei
Group=khoomei
Environment=KHOOMEI_HOME=/var/lib/khoomei
ExecStart=/usr/local/bin/khoomei serve
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/khoomei
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

K="sudo -u khoomei env KHOOMEI_HOME=/var/lib/khoomei khoomei"
$K setup --hub-role --owner <handle> --auth-key-file /var/lib/khoomei/authkey --apply
$K agent muse --node <Muse 节点名> --apply
$K agent grok --node khoomei-<handle>-grok \
    --wake-webhook-url-file /var/lib/khoomei/wake-url --wake-webhook-secret-file /var/lib/khoomei/wake-secret --apply
# authkey / wake-url / wake-secret 各一行，chown khoomei，chmod 600
systemctl enable --now khoomei
journalctl -u khoomei -f
```

日志里看到 `hub x.y.z listening on 100.…:7767 (khoomei-<handle>.….ts.net)` 就是上线了。之后每个请求一行访问日志，唤醒成功或失败也各一行。hub 登录成功后可以去后台吊销那把 auth key；状态在 `tsnet/` 目录里，重启不需要再登录。

## 升级

```sh
curl -fsSLo /tmp/khoomei https://github.com/zjywill/khoomei/releases/latest/download/khoomei-linux-amd64
# 校验同上
install -m 755 /tmp/khoomei /usr/local/bin/khoomei && systemctl restart khoomei
```

配置、`tsnet/`、信箱都不动。先升 hub 再升 caller；协议 `v` 不变，hub 一直接受旧 caller。

## 迁移到另一台机器

节点名要保持不变，Muse 等 caller 才不用改配置：

1. 新机器按上面部署，**不要启动**。
2. 停掉旧 hub；Tailscale 后台删掉旧的 `khoomei-<handle>` 机器（否则新的会叫 `khoomei-<handle>-1`）。
3. 新机器 `systemctl start`。
4. caller 侧：Muse 会把新 hub 当新设备，再弹一次批准卡；其他 caller 不用动。没 ack 的信不会搬过去。

## 常用命令（在 hub 机器上）

```sh
$K doctor                      # 状态
$K agent <name> --node … --apply
$K owner alice --login alice@github --apply   # 朋友
curl -s http://127.0.0.1:7767/health
```
