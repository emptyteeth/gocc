# gocc

gocc 是一个控制局域网里 Chromecast 设备的桌面应用：发现设备、把播放列表里的流地址发到设备上播放，并控制播放与音量。

界面用 [mygo](https://github.com/egoist/mygo) 的原生 UI 绘制（纯 Go，无 cgo，无网页前端），设备通信基于 [vishen/go-chromecast](https://github.com/vishen/go-chromecast)。

## 界面

窗口分三块：

- **区域 1（左）** —— 发现到的设备列表，一次操作一台。点击即连接；`Scan` 重新发现。机器上有多个网卡时（VPN、容器网桥），会多出一个网卡选择框，用来限定在哪个网络上搜索。
- **区域 2（中）** —— 当前设备的状态（播放中/暂停/空闲、媒体标题、URL）与操作：播放/暂停、停止、刷新，以及音量滑块和静音。
- **区域 3（右）** —— 播放列表，条目为「名称 + URL（+ 可选 content type）」。**双击条目**即把它的 URL 发到当前设备播放（选中条目后按回车也一样）。下方表单可新增条目，顶部按钮可上移、下移、删除。

## 运行

需要 Go 1.27+，以及 Linux 上的 GTK 3（原生 UI 的绘制后端）：

```sh
go run .
```

构建：

```sh
go build -o gocc .
```

## 播放列表

播放列表是一个纯文本文件，一行一个条目，字段之间用制表符分隔：

```
# gocc playlist: name<TAB>url[<TAB>content type]
radio1	http://radio1.com/1.mp3
jazz	http://jazz.example/stream	audio/aac
```

空行和以 `#` 开头的行会被忽略。第三个字段（content type）可以省略，此时由 URL 的扩展名推断；扩展名看不出来时才发一个带超时的 HEAD 请求询问，问不出来就交给设备自己猜。

文件位置：应用数据目录下（Linux 上是 `~/.config/gocc/playlist.txt`），首次保存时自动创建。也可以直接手写这个文件——改完重启应用即可。

## 代码结构

| 目录 | 职责 |
| --- | --- |
| `main.go` | 入口：创建窗口，挂载界面 |
| `app/` | 应用状态与界面（`app.go` 是状态与动作，`view.go` 是三块区域的构建） |
| `cast/` | Chromecast：设备发现（`discovery.go`）、单设备控制（`client.go`）、content type 推断（`contenttype.go`） |
| `playlist/` | 播放列表的文本文件读写 |

### 两个值得一提的设计

**设备命令串行化。** 底层库 `vishen/go-chromecast` 不是并发安全的（它自己的源码里就写着 `TODO: nothing here is concurrent safe`）。所以 `cast.Client` 把所有命令排进一个队列，由一个专属 goroutine 按顺序执行，界面线程只管入队、从不等待。`Close` 也等这个 goroutine 停下来之后才关闭连接。

**音量命令节流。** 设备每收到一条音量命令就会响一声，而拖动滑块每一帧都会产生一个新值——一次拖动能产生几十条命令，设备根本处理不过来。所以音量在 `cast.Client` 里做了合并：**每 200ms 最多发一条**，拖动期间中途的值会被后一个值覆盖（只发最新的），手停下来时那个最终值一定会发出。静止一段后的第一次调整立即发送，所以单次点击不会被延迟。静音开关是单次动作，不做节流。

**状态分成两半。** 界面每帧在主线程重建，所以界面自己读写的状态（播放列表、选中项、输入框、滑块位置）不加锁；而设备发现、连接、状态回读都在别的 goroutine 上，它们写的那部分（设备列表、连接、状态）由 `mu` 保护，并通过 `Window.Invalidate` / `Window.Update` 请求重绘。这样界面永远不会为了网络而卡住。

## 测试

```sh
go test ./...
```

界面逻辑用 mygo 的 `ui.Tester` 在无窗口环境下驱动：模拟点击、双击、拖动滑块、输入文字，并断言发往设备的命令。测试里的设备是一个假客户端，不需要真的 Chromecast。

想看一眼界面长什么样（不需要显示器），可以渲染成 PNG：

```sh
GOCC_SNAPSHOT=1 go test ./app/ -run TestRenderSnapshot
# 写到 /tmp/gocc-snapshot.png
```
