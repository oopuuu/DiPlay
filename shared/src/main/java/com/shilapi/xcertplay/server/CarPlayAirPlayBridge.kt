package com.shilapi.xcertplay.server

import com.shilapi.xcertplay.airplay.*
import com.shilapi.xcertplay.iap2.session.Iap2Session
import com.shilapi.xcertplay.mfi.Iap2MfiAuthenticationClient
import com.shilapi.xcertplay.mfi.LocalMfiAuthenticationClient
import com.shilapi.xcertplay.mfi.MfiAuthenticator
import com.shilapi.xcertplay.transport.Iap2IdentificationConfig
import com.shilapi.xcertplay.transport.Iap2WirelessCarPlayEndpoint
import com.shilapi.xcertplay.transport.Iap2WirelessControlClient
import com.shilapi.xcertplay.transport.Iap2WirelessIdentification
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import java.io.BufferedOutputStream
import java.io.File
import java.io.OutputStream
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.NetworkInterface
import java.net.ServerSocket
import java.net.Socket
import java.nio.ByteBuffer
import java.util.UUID
import java.util.concurrent.Executors

object CarPlayAirPlayBridge {
    @Volatile
    private var currentWidth = 1920
    @Volatile
    private var currentHeight = 1080
    private var authDirectory: File? = null

    private fun loadResolution(dir: File) {
        try {
            val f = File(dir, "resolution.conf")
            if (f.exists()) {
                val content = f.readText().trim()
                val parts = content.split("x", ",", " ")
                if (parts.size >= 2) {
                    val w = parts[0].trim().toInt()
                    val h = parts[1].trim().toInt()
                    if (w in 480..3840 && h in 320..2160) {
                        currentWidth = w
                        currentHeight = h
                        println("[AirPlayBridge] Loaded configured resolution: ${w}x${h}")
                    }
                }
            }
        } catch (e: Exception) {
            println("[AirPlayBridge] Error loading resolution: $e")
        }
    }

    private fun getLocalWifiIp(): String {
        try {
            val interfaces = NetworkInterface.getNetworkInterfaces()
            while (interfaces.hasMoreElements()) {
                val iface = interfaces.nextElement()
                if (iface.isUp && !iface.isLoopback && (iface.name.startsWith("wlan") || iface.name.startsWith("ap"))) {
                    val addrs = iface.inetAddresses
                    while (addrs.hasMoreElements()) {
                        val addr = addrs.nextElement()
                        if (addr is java.net.Inet4Address && !addr.isLoopbackAddress) {
                            val ip = addr.hostAddress ?: continue
                            return ip
                        }
                    }
                }
            }
        } catch (_: Exception) {}
        return "100.96.0.1"
    }

    private fun getWifiSsid(): String {
        try {
            val process = ProcessBuilder("sh", "-c", "nmcli -t -f NAME,DEVICE con show --active | grep ':wlan0' | cut -d: -f1")
                .start()
            val ssid = process.inputStream.bufferedReader().readLine()?.trim()
            if (!ssid.isNullOrEmpty() && !ssid.contains("command not found") && !ssid.contains("error")) {
                return ssid
            }
        } catch (_: Exception) {}
        return "Tesla-CarPlay"
    }

    fun wakeUpPairedPhones() {
        Thread {
            try {
                println("[AirPlayBridge] Triggering iAP2 RFCOMM auto-reconnect daemon signal...")
                java.io.File("/tmp/trigger_iap2_reconnect").writeText("1")
            } catch (e: Exception) {
                println("[AirPlayBridge] Bluetooth auto-wake error: $e")
            }
        }.apply {
            isDaemon = true
            name = "BT-Wake-Thread"
            start()
        }
    }

    private fun getBtMac(): String {
        try {
            val process = ProcessBuilder("sh", "-c", "bluetoothctl list | awk '{print \$2}'")
                .start()
            val mac = process.inputStream.bufferedReader().readLine()?.trim()
            val macRegex = Regex("^([0-9A-Fa-f]{2}[:-]){5}([0-9A-Fa-f]{2})$")
            if (!mac.isNullOrEmpty() && macRegex.matches(mac)) {
                return mac.uppercase()
            }
        } catch (_: Exception) {}
        return "B8:27:EB:A9:6F:0A"
    }

    @JvmStatic
    fun main(args: Array<String>) {
        val airplayPort = if (args.isNotEmpty()) args[0].toInt() else 7000
        val videoFeedPort = if (args.size > 1) args[1].toInt() else 7001
        val audioFeedPort = if (args.size > 2) args[2].toInt() else 7002
        val authDir = File(if (args.size > 3) args[3] else "/opt/diplay/auth")
        authDirectory = authDir
        loadResolution(authDir)

        val currentIp = getLocalWifiIp()
        val currentSsid = getWifiSsid()
        val btMac = getBtMac()

        println("======================================================")
        println(">>> DiPlay-Pi Native AirPlay RTSP Server Starting <<<")
        println("  AirPlay Port:     $airplayPort")
        println("  Video Feed Port:  $videoFeedPort")
        println("  Audio Feed Port:  $audioFeedPort")
        println("  Auth Directory:   ${authDir.absolutePath}")
        println("  Resolution:       ${currentWidth}x${currentHeight} @ 60fps")
        println("  Wi-Fi SSID:       $currentSsid")
        println("  Host IP:          $currentIp")
        println("======================================================")
        try { File("/run/diplay_airplay_state").writeText("IDLE\n") } catch (_: Exception) {}

        // 0. Trigger Crypto Engine initialization & self-test
        try {
            val testKey = ByteArray(32)
            val testNonce = ByteArray(12)
            val testPt = "DI_PLAY_CRYPTO_CHECK".toByteArray()
            val sealed = AirPlayCrypto.chachaSeal(testKey, testNonce, testPt)
            val opened = AirPlayCrypto.chachaOpen(testKey, testNonce, sealed)
            if (opened.contentEquals(testPt)) {
                println("[AirPlayBridge] Crypto Engine self-test PASSED successfully!")
            } else {
                println("[AirPlayBridge] Crypto Engine self-test FAILED!")
            }
        } catch (e: Throwable) {
            println("[AirPlayBridge] Crypto Engine self-test error: $e")
        }

        // 1. Load or generate Ed25519 identity key
        val ed25519KeyFile = File(authDir, "ed25519_identity.key")
        val identity = if (ed25519KeyFile.exists() && ed25519KeyFile.length() == 32L) {
            val privBytes = ed25519KeyFile.readBytes()
            val privParams = Ed25519PrivateKeyParameters(privBytes, 0)
            val pubBytes = privParams.generatePublicKey().encoded
            println("[AirPlayBridge] Loaded existing Ed25519 identity key (${privBytes.size} bytes)")
            AirPlayIdentity(privBytes, pubBytes, UUID.randomUUID().toString())
        } else {
            println("[AirPlayBridge] Generating new Ed25519 identity key...")
            val newIdent = AirPlayIdentity.generate()
            authDir.mkdirs()
            ed25519KeyFile.writeBytes(newIdent.privateKey)
            newIdent
        }

        // 2. Load MFi Authenticator
        val mfi: MfiAuthenticator? = try {
            if (File(authDir, "certificate.p7b").exists() && File(authDir, "identity.pk8").exists()) {
                val client = LocalMfiAuthenticationClient.load(authDir)
                println("[AirPlayBridge] Successfully loaded Local MFi Authenticator from $authDir")
                client
            } else {
                println("[AirPlayBridge] Warning: No MFi credentials found in $authDir, running in mock mode")
                null
            }
        } catch (e: Exception) {
            println("[AirPlayBridge] Error loading MFi credentials: $e")
            null
        }

        // 3. Configure AirPlay display settings for Tesla
        val displayConfig = AirPlayDisplayConfig(
            widthPixels = 1920,
            heightPixels = 1080,
            fps = 60,
        )
        val airPlayConfig = AirPlayConfig(
            deviceName = "Tesla-CarPlay",
            deviceId = btMac,
            btMac = btMac,
            sourceVersion = "366.0",
            main = displayConfig,
            port = airplayPort,
            manufacturer = "Tesla",
            model = "Tesla-CarPlay",
            oemLabel = "Tesla",
        )

        // 4. Create Forwarding Media Sink that writes H.264 NALUs to port 7001
        val sink = object : MediaSink {
            private var videoSocket: Socket? = null
            private var videoOut: OutputStream? = null
            private var frameCount = 0L
            private var totalBytes = 0L
            private var lastLogTime = System.currentTimeMillis()

            @Synchronized
            private fun getOut(): OutputStream? {
                if (videoOut != null) return videoOut
                try {
                    val s = Socket("127.0.0.1", videoFeedPort)
                    s.tcpNoDelay = true
                    videoSocket = s
                    videoOut = BufferedOutputStream(s.getOutputStream(), 64 * 1024)
                    println("[MediaSink] >>> Successfully connected to video feed at 127.0.0.1:$videoFeedPort <<<")
                } catch (e: Exception) {
                    println("[MediaSink] Waiting to connect to 127.0.0.1:$videoFeedPort: $e")
                }
                return videoOut
            }

            override fun onVideoConfig(type: Int, codecData: ByteArray) {
                println("[MediaSink] >>> Received Video Config (SPS/PPS) len=${codecData.size} <<<")
                onVideoFrame(type, codecData)
            }

            override fun onVideoFrame(type: Int, naluBytes: ByteArray) {
                frameCount++
                totalBytes += naluBytes.size

                val now = System.currentTimeMillis()
                if (frameCount <= 5 || frameCount % 120 == 0L || now - lastLogTime >= 5000) {
                    val hexHead = naluBytes.take(16).joinToString("") { "%02X".format(it) }
                    println("[MediaSink] Frame #$frameCount (type=$type len=${naluBytes.size}B, total=${totalBytes / 1024}KB, hex=$hexHead)")
                    lastLogTime = now
                }

                try {
                    val out = getOut() ?: return
                    val lenBuf = ByteBuffer.allocate(4).putInt(naluBytes.size).array()
                    out.write(lenBuf)
                    out.write(naluBytes)
                    out.flush()
                } catch (e: Exception) {
                    println("[MediaSink] Video write error: $e, reconnecting...")
                    try { videoSocket?.close() } catch (_: Exception) {}
                    videoOut = null
                    videoSocket = null
                }
            }

            override fun onScreenStreamActive(type: Int, active: Boolean) {
                println("[MediaSink] Screen stream active: $active (type=$type)")
            }
        }

        val media = CarPlayMediaEngine(sink)
        val pairings = PairingStore()

        // 5. Setup iAP2 Tunnel Handler for Stream Type 130
        val identification = Iap2IdentificationConfig(
            name = "Tesla-CarPlay",
            modelIdentifier = "Tesla-CarPlay",
            manufacturer = "Tesla",
            serialNumber = "000000000001",
            firmwareVersion = "1.0.0",
            hardwareVersion = "1.0.0",
            carPlayUsbInterfaceNumber = 1,
            wireless = Iap2WirelessIdentification(btMac, currentSsid),
        )

        val passphrase = if (currentSsid == "Tesla-CarPlay") "diplay123456" else "zhong497765803"
        val channel = if (currentSsid == "Tesla-CarPlay") 36 else 1

        val endpoint = Iap2WirelessCarPlayEndpoint(
            ssid = currentSsid,
            passphrase = passphrase,
            channel = channel,
            security = com.shilapi.xcertplay.transport.Iap2WirelessSecurity.WPA_WPA2,
            ipAddresses = listOf(currentIp),
            airPlayPort = airplayPort,
            deviceIdentifier = airPlayConfig.deviceId,
            publicKey = identity.publicKeyHex,
            sourceVersion = airPlayConfig.sourceVersion,
        )

        val iapExecutor = Executors.newCachedThreadPool()

        media.setIapTunnelHandler { stream ->
            println("[AirPlayBridge] >>> iPhone connected to iAP2 tunnel (stream 130)! Launching iAP2 Control Client <<<")
            if (mfi == null) {
                println("[AirPlayBridge] Warning: No MFi authenticator loaded for iAP tunnel")
                return@setIapTunnelHandler false
            }
            try {
                val channel = Iap2Session.openTunnel(
                    stream,
                    traceContext = "wireless-tunnel",
                    onTrace = { msg -> println("[iAP-Tunnel] $msg") },
                )
                iapExecutor.submit {
                    try {
                        val controlClient = Iap2WirelessControlClient(
                            session = channel,
                            mfi = Iap2MfiAuthenticationClient(mfi),
                        )
                        println("[iAP-Tunnel] Running Iap2WirelessControlClient identification & auth loop...")
                        val result = controlClient.run(
                            identification = identification,
                            endpoint = endpoint,
                            timeoutMillis = Iap2WirelessControlClient.NO_TIMEOUT_MILLIS,
                            isTunnel = true,
                            onReady = {
                                println("==================================================================")
                                println(">>> [iAP-Tunnel] WIRELESS iAP2 TUNNEL READY! IPHONE AUTHORIZED <<<")
                                println("==================================================================")
                            },
                            onIncoming = { frame ->
                                // println("[iAP-Tunnel] Inbound message: ${frame.messageId}")
                            },
                            onProgress = { msg ->
                                println("[iAP-Tunnel] Progress: $msg")
                            },
                        )
                        println("[iAP-Tunnel] Session finished terminal=${result.terminal} stage=${result.stage}")
                    } catch (e: Exception) {
                        println("[iAP-Tunnel] Error in iAP2 control session: $e")
                        e.printStackTrace()
                    }
                }
                true
            } catch (e: Exception) {
                println("[AirPlayBridge] Failed to open iAP tunnel: $e")
                e.printStackTrace()
                false
            }
        }

        val activeSession = java.util.concurrent.atomic.AtomicReference<AirPlaySession?>()

        val listener = object : AirPlaySessionListener {
            override fun onSessionActive(session: AirPlaySession) {
                println("[AirPlayBridge] >>> CarPlay AirPlay Session ACTIVE! Streaming started! <<<")
                activeSession.set(session)
                try { java.io.File("/run/diplay_airplay_state").writeText("ACTIVE\n") } catch (_: Exception) {}
                try {
                    val sent = session.sendCommand(mapOf("type" to "forceKeyFrame"))
                    println("[AirPlayBridge] Sent initial forceKeyFrame request to iPhone: $sent")
                } catch (e: Exception) {
                    println("[AirPlayBridge] Failed to send initial forceKeyFrame: $e")
                }
            }
            override fun onVideoFrameRendered(session: AirPlaySession) {
                // Frame rendered
            }
            override fun onSessionEnded(session: AirPlaySession) {
                println("[AirPlayBridge] CarPlay AirPlay Session Ended.")
                activeSession.compareAndSet(session, null)
                try { java.io.File("/run/diplay_airplay_state").writeText("IDLE\n") } catch (_: Exception) {}
            }
            override fun onTransportError(message: String) {
                println("[AirPlayBridge] Transport error: $message")
                try { java.io.File("/run/diplay_airplay_state").writeText("IDLE\n") } catch (_: Exception) {}
            }
            override fun onCommand(session: AirPlaySession, type: String, params: Map<String, Any?>) {
                println("[AirPlayBridge] >>> Received AirPlay Command: type=$type, params=$params <<<")
                if (type.equals("disableBluetooth", ignoreCase = true) || type.equals("disable-bluetooth", ignoreCase = true)) {
                    val rawDeviceId = params["deviceID"] as? String ?: btMac
                    val deviceMac = rawDeviceId.replace("-", ":").uppercase()
                    println("[AirPlayBridge] >>> Apple Wireless CarPlay Handoff: Disconnecting Bluetooth for $deviceMac <<<")
                    Thread {
                        try {
                            Thread.sleep(200)
                            ProcessBuilder("bluetoothctl", "disconnect", deviceMac).start()
                        } catch (e: Exception) {
                            println("[AirPlayBridge] Failed to disconnect Bluetooth: $e")
                        }
                    }.start()
                }
                if (type == "requestKeyFrame") {
                    try {
                        val sent = session.sendCommand(mapOf("type" to "forceKeyFrame"))
                        println("[AirPlayBridge] Answered requestKeyFrame with forceKeyFrame: $sent")
                    } catch (e: Exception) {
                        println("[AirPlayBridge] Failed to send forceKeyFrame: $e")
                    }
                }
            }
        }

        // 5. Start Local Touch & Control Listener on 127.0.0.1:7003
        val controlPort = if (args.size > 4) args[4].toInt() else 7003
        kotlin.concurrent.thread(isDaemon = true, name = "CarPlay-Touch-Control-Listener") {
            try {
                val controlServer = ServerSocket()
                controlServer.reuseAddress = true
                controlServer.bind(InetSocketAddress("127.0.0.1", controlPort))
                println("[AirPlayBridge] Listening for Web Touch & Actions on 127.0.0.1:$controlPort...")
                while (true) {
                    val sock = controlServer.accept()
                    sock.tcpNoDelay = true
                    kotlin.concurrent.thread(isDaemon = true) {
                        try {
                            val input = sock.getInputStream()
                            while (true) {
                                val b0 = input.read()
                                if (b0 == -1) break
                                if (b0 == 0x54) { // 'T': Ultra-fast Binary Touch Packet
                                    val count = input.read()
                                    if (count == -1) break
                                    val contacts = if (count == 0) {
                                        emptyList()
                                    } else {
                                        val cCount = count.coerceAtMost(2)
                                        val buf = ByteArray(6 * cCount)
                                        var readTotal = 0
                                        while (readTotal < buf.size) {
                                            val r = input.read(buf, readTotal, buf.size - readTotal)
                                            if (r == -1) break
                                            readTotal += r
                                        }
                                        if (readTotal == buf.size) {
                                            val list = ArrayList<AirPlayContact>(cCount)
                                            for (i in 0 until cCount) {
                                                val off = i * 6
                                                val id = buf[off].toInt() and 0xFF
                                                val down = (buf[off + 1].toInt() and 0x01) != 0
                                                val xInt = ((buf[off + 2].toInt() and 0xFF) shl 8) or (buf[off + 3].toInt() and 0xFF)
                                                val yInt = ((buf[off + 4].toInt() and 0xFF) shl 8) or (buf[off + 5].toInt() and 0xFF)
                                                val x = xInt.toDouble() / 65535.0
                                                val y = yInt.toDouble() / 65535.0
                                                list.add(AirPlayContact(id, x, y, down))
                                            }
                                            list
                                        } else {
                                            emptyList()
                                        }
                                    }
                                    activeSession.get()?.sendTouch(contacts)
                                } else {
                                    val lineBuf = java.io.ByteArrayOutputStream()
                                    if (b0 != '\n'.code && b0 != '\r'.code) {
                                        lineBuf.write(b0)
                                    }
                                    while (true) {
                                        val ch = input.read()
                                        if (ch == -1) break
                                        if (ch == '\n'.code) break
                                        if (ch != '\r'.code) {
                                            lineBuf.write(ch)
                                        }
                                    }
                                    val line = lineBuf.toString(Charsets.UTF_8.name()).trim()
                                    if (line.isNotEmpty()) {
                                        handleControlCommand(line, activeSession.get())
                                    }
                                }
                            }
                        } catch (_: Exception) {} finally {
                            try { sock.close() } catch (_: Exception) {}
                        }
                    }
                }
            } catch (e: Exception) {
                println("[AirPlayBridge] Control Server error: $e")
            }
        }

        // 6. Start ServerSocket on 0.0.0.0:airplayPort
        val server = ServerSocket()
        server.reuseAddress = true
        server.bind(InetSocketAddress("0.0.0.0", airplayPort))
        println("[AirPlayBridge] Listening for iPhone AirPlay connections on 0.0.0.0:$airplayPort...")

        // Auto-wake paired iPhone via Bluetooth on startup
        wakeUpPairedPhones()

        val executor = Executors.newCachedThreadPool()

        while (true) {
            try {
                val clientSocket = server.accept()
                clientSocket.tcpNoDelay = true
                clientSocket.keepAlive = true
                println("[AirPlayBridge] Accepted AirPlay connection from ${clientSocket.remoteSocketAddress}")

                executor.submit {
                    try {
                        val sessionDisplayConfig = AirPlayDisplayConfig(
                            widthPixels = currentWidth,
                            heightPixels = currentHeight,
                            fps = 60,
                        )
                        val sessionConfig = airPlayConfig.copy(main = sessionDisplayConfig)
                        println("[AirPlayBridge] >>> Launching AirPlaySession with negotiated display: ${currentWidth}x${currentHeight} @ 60 FPS <<<")
                        val session = AirPlaySession(
                            socket = clientSocket,
                            config = sessionConfig,
                            identity = identity,
                            pairings = pairings,
                            mfi = mfi,
                            listener = listener,
                            media = media,
                        )
                        session.start()
                    } catch (e: Exception) {
                        println("[AirPlayBridge] Session error: $e")
                        e.printStackTrace()
                    }
                }
            } catch (e: Exception) {
                println("[AirPlayBridge] Accept error: $e")
            }
        }
    }

    private fun handleControlCommand(jsonStr: String, session: AirPlaySession?) {
        if (jsonStr.contains("\"type\":\"restart_session\"") || jsonStr.contains("\"type\": \"restart_session\"")) {
            println("[AirPlayBridge] Received RESTART_SESSION command: closing active session to trigger resolution renegotiation...")
            try {
                session?.close()
            } catch (e: Exception) {
                println("[AirPlayBridge] Error closing session on restart: $e")
            }
            wakeUpPairedPhones()
            return
        }
        if (jsonStr.contains("\"type\":\"reconnect\"") || jsonStr.contains("\"type\": \"reconnect\"")) {
            if (session != null) {
                println("[AirPlayBridge] Active AirPlay session is already established. Skipping reconnect.")
                return
            }
            println("[AirPlayBridge] Received RECONNECT command: triggering Bluetooth auto-wake...")
            wakeUpPairedPhones()
            return
        }
        if (jsonStr.contains("\"type\":\"resolution\"") || jsonStr.contains("\"type\": \"resolution\"")) {
            try {
                val wMatch = Regex(""""width"\s*:\s*(\d+)""").find(jsonStr)
                val hMatch = Regex(""""height"\s*:\s*(\d+)""").find(jsonStr)
                if (wMatch != null && hMatch != null) {
                    val newW = wMatch.groupValues[1].toInt()
                    val newH = hMatch.groupValues[1].toInt()
                    if (newW in 480..3840 && newH in 320..2160) {
                        currentWidth = newW
                        currentHeight = newH
                        println("[AirPlayBridge] >>> Resolution preference set to ${currentWidth}x${currentHeight}. Persisted for next connection. <<<")
                        authDirectory?.let { dir ->
                            try {
                                File(dir, "resolution.conf").writeText("${currentWidth}x${currentHeight}")
                            } catch (e: Exception) {
                                println("[AirPlayBridge] Failed to write resolution.conf: $e")
                            }
                        }
                    }
                }
            } catch (e: Exception) {
                println("[AirPlayBridge] Error handling resolution command: $e")
            }
            return
        }
        if (session == null) {
            println("[AirPlayBridge] Cannot handle control command: activeSession is null")
            return
        }
        try {
            if (jsonStr.contains("\"type\":\"touch\"") || jsonStr.contains("\"type\": \"touch\"") || jsonStr.contains("\"contacts\"")) {
                val contacts = parseContactsFromJson(jsonStr)
                session.sendTouch(contacts)
            } else if (jsonStr.contains("\"type\":\"action\"") || jsonStr.contains("\"type\": \"action\"")) {
                if (jsonStr.contains("\"home\"")) {
                    println("[AirPlayBridge] Action: HOME")
                    session.sendKnob(AirPlayKnobState(home = true), momentary = true)
                } else if (jsonStr.contains("play_pause")) {
                    println("[AirPlayBridge] Action: PLAY/PAUSE")
                    session.sendMedia(1)
                } else if (jsonStr.contains("next")) {
                    println("[AirPlayBridge] Action: NEXT")
                    session.sendMedia(3)
                } else if (jsonStr.contains("prev")) {
                    println("[AirPlayBridge] Action: PREV")
                    session.sendMedia(4)
                }
            } else if (jsonStr.contains("\"type\":\"keyframe\"") || jsonStr.contains("\"type\": \"keyframe\"")) {
                println("[AirPlayBridge] Action: FORCE KEYFRAME")
                val sent = session.sendCommand(mapOf("type" to "forceKeyFrame"))
                println("[AirPlayBridge] Force KeyFrame sent=$sent")
            }
        } catch (e: Exception) {
            println("[AirPlayBridge] Error handling control command: $e")
        }
    }

    private fun parseContactsFromJson(jsonStr: String): List<AirPlayContact> {
        val list = mutableListOf<AirPlayContact>()
        val regex = Regex("""\{[^}]*?"id"\s*:\s*(\d+)[^}]*?"x"\s*:\s*([0-9.]+)[^}]*?"y"\s*:\s*([0-9.]+)[^}]*?"down"\s*:\s*(true|false)[^}]*?\}""")
        for (m in regex.findAll(jsonStr)) {
            val id = m.groupValues[1].toIntOrNull() ?: 0
            val x = m.groupValues[2].toDoubleOrNull() ?: 0.0
            val y = m.groupValues[3].toDoubleOrNull() ?: 0.0
            val down = m.groupValues[4].toBoolean()
            list.add(AirPlayContact(id, x, y, down))
        }
        return list
    }
}

