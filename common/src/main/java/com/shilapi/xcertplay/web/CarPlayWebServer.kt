package com.shilapi.xcertplay.web

import android.util.Log
import com.shilapi.xcertplay.airplay.AirPlayContact
import com.shilapi.xcertplay.airplay.VideoCodec
import com.shilapi.xcertplay.media.CarPlayVideoBridge
import org.json.JSONArray
import org.json.JSONObject
import java.io.BufferedInputStream
import java.io.BufferedReader
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.io.InputStreamReader
import java.io.OutputStream
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketException
import java.util.Collections
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Lightweight, zero-dependency embedded HTTP server providing:
 * - Web Remote HTML application
 * - Live MJPEG stream for modern browsers
 * - High-resolution snapshot endpoints
 * - Low-latency touch and action APIs
 */
class CarPlayWebServer(
    val port: Int,
    private val onTouch: (List<AirPlayContact>) -> Unit,
    private val onAction: (String) -> Unit,
    private val onResolution: (Int, Int) -> Unit = { _, _ -> },
    private val getStatus: () -> ServerStatus,
    var onMicData: ((ByteArray) -> Unit)? = null,
    var onClientCountChanged: ((Int) -> Unit)? = null,
) {
    data class ServerStatus(
        val active: Boolean,
        val hasSession: Boolean,
        val width: Int,
        val height: Int,
        val fps: Int = 0,
    )

    private val running = AtomicBoolean(false)
    private var serverSocket: ServerSocket? = null
    private val executor: ExecutorService = Executors.newCachedThreadPool { runnable ->
        Thread(runnable, "carplay-web-http").apply { isDaemon = true }
    }

    private val streamClients = Collections.newSetFromMap(ConcurrentHashMap<MjpegSession, Boolean>())
    private val audioClients = Collections.newSetFromMap(ConcurrentHashMap<OutputStream, Boolean>())
    private val wsClients = Collections.newSetFromMap(ConcurrentHashMap<WsSession, Boolean>())
    private val wsAudioClients = Collections.newSetFromMap(ConcurrentHashMap<WsSession, Boolean>())
    @Volatile private var latestFrame: ByteArray? = null

    val activeClientCount: Int
        get() = streamClients.size + audioClients.size + wsClients.size + wsAudioClients.size

    fun hasStreamClients(): Boolean = streamClients.isNotEmpty()
    fun hasAudioClients(): Boolean = audioClients.isNotEmpty() || wsClients.isNotEmpty() || wsAudioClients.isNotEmpty()
    fun hasWsClients(): Boolean = wsClients.isNotEmpty() || wsAudioClients.isNotEmpty()

    private fun notifyClientsChanged() {
        onClientCountChanged?.invoke(activeClientCount)
    }

    fun start(): Boolean {
        if (running.get()) return true
        return try {
            val srv = ServerSocket(port)
            serverSocket = srv
            running.set(true)
            executor.execute {
                while (running.get() && !srv.isClosed) {
                    try {
                        val client = srv.accept()
                        executor.execute { handleClient(client) }
                    } catch (e: Exception) {
                        if (!running.get()) break
                        Log.w(TAG, "Server socket accept encountered error", e)
                    }
                }
            }
            Log.i(TAG, "CarPlayWebServer started on port $port")
            true
        } catch (e: Exception) {
            Log.e(TAG, "Failed to start CarPlayWebServer on port $port", e)
            false
        }
    }

    fun stop() {
        if (!running.compareAndSet(true, false)) return
        try {
            serverSocket?.close()
        } catch (_: Exception) {}
        serverSocket = null
        for (client in streamClients) {
            client.close()
        }
        for (client in audioClients) {
            try { client.close() } catch (_: Exception) {}
        }
        for (client in wsClients) {
            client.close()
        }
        for (client in wsAudioClients) {
            client.close()
        }
        streamClients.clear()
        audioClients.clear()
        wsClients.clear()
        wsAudioClients.clear()
        try { executor.shutdownNow() } catch (_: Exception) {}
        notifyClientsChanged()
        Log.i(TAG, "CarPlayWebServer stopped")
    }

    /** Broadcasts a new JPEG frame to all connected WebSocket clients (Channel 0x03) and legacy MJPEG stream clients. */
    fun broadcastFrame(jpegBytes: ByteArray) {
        latestFrame = jpegBytes

        // Fallback to legacy HTTP MJPEG stream clients (/stream)
        if (streamClients.isNotEmpty()) {
            val dead = mutableListOf<MjpegSession>()
            for (session in streamClients) {
                if (!session.offerFrame(jpegBytes)) {
                    dead.add(session)
                }
            }
            if (dead.isNotEmpty()) {
                streamClients.removeAll(dead.toSet())
                notifyClientsChanged()
            }
        }
    }

    /** Broadcasts PCM audio data to both dedicated audio WebSocket clients, shared WebSocket clients, and HTTP clients. */
    fun broadcastAudio(pcm: ByteArray, offset: Int, length: Int, sampleRate: Int = 44100, channels: Int = 2) {
        if (!hasAudioClients() || length <= 0) return

        val rateCode = when (sampleRate) {
            16000 -> 1
            24000 -> 2
            48000 -> 3
            else -> 0 // 44100
        }
        val chBit = if (channels == 1) 0x08 else 0
        val flagByte = (rateCode or chBit).toByte()

        // 1. Send to dedicated audio WebSocket clients (ZERO contention with video stream!)
        if (wsAudioClients.isNotEmpty()) {
            val dead = mutableListOf<WsSession>()
            for (ws in wsAudioClients) {
                if (!ws.sendMultiplexBinary(0x00.toByte(), flagByte, pcm, offset, length)) {
                    dead.add(ws)
                }
            }
            if (dead.isNotEmpty()) {
                wsAudioClients.removeAll(dead.toSet())
                notifyClientsChanged()
            }
        }

        // 2. Also send to main WebSocket clients as fallback
        if (wsClients.isNotEmpty()) {
            val deadWs = mutableListOf<WsSession>()
            for (ws in wsClients) {
                if (!ws.sendMultiplexBinary(0x00.toByte(), flagByte, pcm, offset, length)) {
                    deadWs.add(ws)
                }
            }
            if (deadWs.isNotEmpty()) {
                wsClients.removeAll(deadWs.toSet())
                notifyClientsChanged()
            }
        }

        // 2. HTTP chunked audio broadcast fallback
        if (audioClients.isNotEmpty()) {
            val chunk = if (offset == 0 && length == pcm.size) pcm else pcm.copyOfRange(offset, offset + length)
            val lenHex = (Integer.toHexString(chunk.size) + "\r\n").toByteArray(Charsets.US_ASCII)
            val crlf = "\r\n".toByteArray(Charsets.US_ASCII)

            val dead = mutableListOf<OutputStream>()
            for (stream in audioClients) {
                try {
                    stream.write(lenHex)
                    stream.write(chunk)
                    stream.write(crlf)
                    stream.flush()
                } catch (_: Exception) {
                    dead.add(stream)
                }
            }
            if (dead.isNotEmpty()) {
                audioClients.removeAll(dead.toSet())
                notifyClientsChanged()
            }
        }
    }

    /** Broadcasts H.264 / H.265 video config (avcC / SPS+PPS) to WebSocket clients via Channel 0x01 */
    fun broadcastVideoConfig(codec: VideoCodec, codecData: ByteArray) {
        if (wsClients.isEmpty() || codecData.isEmpty()) return
        val flag: Byte = if (codec == VideoCodec.H265) 1 else 0
        val deadWs = mutableListOf<WsSession>()
        for (ws in wsClients) {
            if (!ws.sendMultiplexBinary(0x01.toByte(), flag, codecData, 0, codecData.size)) {
                deadWs.add(ws)
            }
        }
        if (deadWs.isNotEmpty()) {
            wsClients.removeAll(deadWs.toSet())
            notifyClientsChanged()
        }
    }

    /** Broadcasts raw H.264 / H.265 video frame (NAL units) to WebSocket clients via Channel 0x02 */
    fun broadcastVideoFrame(isKeyFrame: Boolean, naluBytes: ByteArray) {
        if (wsClients.isEmpty() || naluBytes.isEmpty()) return
        val flag: Byte = if (isKeyFrame) 1 else 0
        val deadWs = mutableListOf<WsSession>()
        for (ws in wsClients) {
            if (!ws.sendMultiplexBinary(0x02.toByte(), flag, naluBytes, 0, naluBytes.size)) {
                deadWs.add(ws)
            }
        }
        if (deadWs.isNotEmpty()) {
            wsClients.removeAll(deadWs.toSet())
            notifyClientsChanged()
        }
    }

    private fun handleClient(socket: Socket) {
        try {
            socket.tcpNoDelay = true
            socket.soTimeout = 15000
            val input = BufferedInputStream(socket.getInputStream())
            val output = socket.getOutputStream()

            val requestLine = readAsciiLine(input) ?: return
            val parts = requestLine.split(" ")
            if (parts.size < 2) return

            val method = parts[0].uppercase()
            val rawPath = parts[1]
            val path = if (rawPath.contains("?")) rawPath.substring(0, rawPath.indexOf("?")) else rawPath

            // Read headers
            val headers = mutableMapOf<String, String>()
            var contentLength = 0
            while (true) {
                val line = readAsciiLine(input) ?: break
                if (line.isEmpty()) break
                val colon = line.indexOf(':')
                if (colon > 0) {
                    val k = line.substring(0, colon).trim().lowercase()
                    val v = line.substring(colon + 1).trim()
                    headers[k] = v
                    if (k == "content-length") {
                        contentLength = v.toIntOrNull() ?: 0
                    }
                }
            }

            // Check WebSocket Upgrade header
            val upgrade = headers["upgrade"]?.lowercase()
            val secWsKey = headers["sec-websocket-key"]
            if (upgrade == "websocket" && secWsKey != null) {
                handleWebSocketUpgrade(socket, input, output, secWsKey, path)
                return
            }

            if (method == "OPTIONS") {
                sendCors(output, 204, "No Content")
                return
            }

            when (path) {
                "/" -> {
                    val html = WebRemoteHtml.getHtml(port)
                    val data = html.toByteArray(Charsets.UTF_8)
                    sendResponse(output, 200, "OK", "text/html; charset=utf-8", data)
                }
                "/stream" -> {
                    socket.soTimeout = 0
                    val boundary = "frame"
                    val responseHeader = (
                        "HTTP/1.1 200 OK\r\n" +
                        "Connection: close\r\n" +
                        "Server: DiPlay-WebRemote\r\n" +
                        "Cache-Control: no-cache, no-store, must-revalidate, pre-check=0, post-check=0, max-age=0\r\n" +
                        "Pragma: no-cache\r\n" +
                        "Access-Control-Allow-Origin: *\r\n" +
                        "Content-Type: multipart/x-mixed-replace; boundary=--$boundary\r\n\r\n"
                    ).toByteArray(Charsets.US_ASCII)
                    output.write(responseHeader)
                    output.flush()

                    val session = MjpegSession(socket, output)
                    streamClients.add(session)
                    notifyClientsChanged()

                    latestFrame?.let { session.offerFrame(it) }
                    session.runLoop(running) {
                        streamClients.remove(session)
                        notifyClientsChanged()
                    }
                }
                "/audio" -> {
                    socket.soTimeout = 0
                    val responseHeader = (
                        "HTTP/1.1 200 OK\r\n" +
                        "Connection: close\r\n" +
                        "Server: DiPlay-WebRemote\r\n" +
                        "Cache-Control: no-cache, no-store, must-revalidate\r\n" +
                        "Pragma: no-cache\r\n" +
                        "Access-Control-Allow-Origin: *\r\n" +
                        "Content-Type: audio/wav\r\n" +
                        "Transfer-Encoding: chunked\r\n\r\n"
                    ).toByteArray(Charsets.US_ASCII)
                    output.write(responseHeader)

                    val wavHeader = createWavHeader(44100, 2)
                    val lenHex = (Integer.toHexString(wavHeader.size) + "\r\n").toByteArray(Charsets.US_ASCII)
                    output.write(lenHex)
                    output.write(wavHeader)
                    output.write("\r\n".toByteArray(Charsets.US_ASCII))
                    output.flush()

                    audioClients.add(output)
                    notifyClientsChanged()
                    try {
                        while (running.get() && !socket.isClosed) {
                            Thread.sleep(1000)
                        }
                    } catch (_: Exception) {} finally {
                        audioClients.remove(output)
                        notifyClientsChanged()
                    }
                }
                "/api/mic" -> {
                    if (method == "POST" && contentLength > 0) {
                        val body = ByteArray(contentLength)
                        var readTotal = 0
                        while (readTotal < contentLength) {
                            val r = input.read(body, readTotal, contentLength - readTotal)
                            if (r <= 0) break
                            readTotal += r
                        }
                        if (readTotal > 0) {
                            onMicData?.invoke(if (readTotal == contentLength) body else body.copyOf(readTotal))
                        }
                        sendResponse(output, 200, "OK", "application/json", "{\"ok\":true}".toByteArray(Charsets.UTF_8))
                    } else {
                        sendResponse(output, 400, "Bad Request", "text/plain", "Bad Request".toByteArray(Charsets.UTF_8))
                    }
                }
                "/snapshot" -> {
                    val frame = latestFrame
                    if (frame != null) {
                        sendResponse(output, 200, "OK", "image/jpeg", frame)
                    } else {
                        val empty = createEmptyJpeg()
                        sendResponse(output, 200, "OK", "image/jpeg", empty)
                    }
                }
                "/api/status" -> {
                    val status = getStatus()
                    val json = JSONObject().apply {
                        put("active", status.active)
                        put("hasSession", status.hasSession)
                        put("width", status.width)
                        put("height", status.height)
                        put("fps", status.fps)
                        put("streamClients", streamClients.size)
                        put("audioClients", audioClients.size)
                    }
                    sendResponse(output, 200, "OK", "application/json", json.toString().toByteArray(Charsets.UTF_8))
                }
                "/api/touch" -> {
                    val body = readBody(input, contentLength)
                    try {
                        val obj = JSONObject(body)
                        val arr = obj.optJSONArray("contacts") ?: JSONArray()
                        val list = mutableListOf<AirPlayContact>()
                        for (i in 0 until arr.length()) {
                            val c = arr.getJSONObject(i)
                            list.add(
                                AirPlayContact(
                                    id = c.optInt("id", i),
                                    x = c.optDouble("x", 0.0),
                                    y = c.optDouble("y", 0.0),
                                    down = c.optBoolean("down", false),
                                )
                            )
                        }
                        onTouch(list)
                        sendResponse(output, 200, "OK", "application/json", "{\"ok\":true}".toByteArray(Charsets.UTF_8))
                    } catch (e: Exception) {
                        sendResponse(output, 400, "Bad Request", "text/plain", e.message?.toByteArray() ?: ByteArray(0))
                    }
                }
                "/api/action" -> {
                    val body = readBody(input, contentLength)
                    try {
                        val obj = JSONObject(body)
                        val action = obj.optString("action", "")
                        if (action.isNotEmpty()) {
                            onAction(action)
                        }
                        sendResponse(output, 200, "OK", "application/json", "{\"ok\":true}".toByteArray(Charsets.UTF_8))
                    } catch (e: Exception) {
                        sendResponse(output, 400, "Bad Request", "text/plain", e.message?.toByteArray() ?: ByteArray(0))
                    }
                }
                "/api/resolution" -> {
                    val body = readBody(input, contentLength)
                    try {
                        val obj = JSONObject(body)
                        val width = obj.optInt("width", 0)
                        val height = obj.optInt("height", 0)
                        if (width in 400..3840 && height in 300..2160) {
                            onResolution(width, height)
                            sendResponse(output, 200, "OK", "application/json", "{\"ok\":true,\"width\":$width,\"height\":$height}".toByteArray(Charsets.UTF_8))
                        } else {
                            sendResponse(output, 400, "Bad Request", "text/plain", "Invalid resolution dimensions".toByteArray(Charsets.UTF_8))
                        }
                    } catch (e: Exception) {
                        sendResponse(output, 400, "Bad Request", "text/plain", e.message?.toByteArray() ?: ByteArray(0))
                    }
                }
                else -> {
                    sendResponse(output, 404, "Not Found", "text/plain", "Not Found".toByteArray(Charsets.UTF_8))
                }
            }
        } catch (_: SocketException) {
            // Normal client disconnect
        } catch (e: Exception) {
            Log.d(TAG, "HTTP client handling exception", e)
        } finally {
            try { socket.close() } catch (_: Exception) {}
        }
    }

    private fun handleWebSocketUpgrade(
        socket: Socket,
        input: InputStream,
        output: OutputStream,
        secWsKey: String,
        path: String,
    ) {
        try {
            socket.soTimeout = 0 // WebSocket needs infinite read timeout
            val raw = secWsKey.trim() + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
            val md = java.security.MessageDigest.getInstance("SHA-1")
            val sha1 = md.digest(raw.toByteArray(Charsets.US_ASCII))
            val accept = java.util.Base64.getEncoder().encodeToString(sha1)

            val handshakeResp = (
                "HTTP/1.1 101 Switching Protocols\r\n" +
                "Upgrade: websocket\r\n" +
                "Connection: Upgrade\r\n" +
                "Sec-WebSocket-Accept: $accept\r\n\r\n"
            ).toByteArray(Charsets.US_ASCII)
            output.write(handshakeResp)
            output.flush()

            val isAudioDedicated = path == "/audio_ws"
            val session = WsSession(socket, output)
            if (isAudioDedicated) {
                wsAudioClients.add(session)
            } else {
                wsClients.add(session)
            }
            notifyClientsChanged()

            if (!isAudioDedicated) {
                // Send initial connected greeting / server status
                val status = getStatus()
                val initJson = JSONObject().apply {
                    put("type", "hello")
                    put("active", status.active)
                    put("hasSession", status.hasSession)
                    put("width", status.width)
                    put("height", status.height)
                    put("fps", status.fps)
                }
                session.sendText(initJson.toString())

                // If we have cached video config and IDR keyframe, immediately push them to the new client!
                val lastCodecData = CarPlayVideoBridge.lastCodecData
                if (lastCodecData != null) {
                    val flag: Byte = if (CarPlayVideoBridge.lastCodec == VideoCodec.H265) 1 else 0
                    session.sendMultiplexBinary(0x01.toByte(), flag, lastCodecData, 0, lastCodecData.size)
                }
                val lastIdr = CarPlayVideoBridge.lastIdrFrame
                if (lastIdr != null) {
                    session.sendMultiplexBinary(0x02.toByte(), 1.toByte(), lastIdr, 0, lastIdr.size)
                }
                CarPlayVideoBridge.requestKeyFrame()
            } else {
                session.sendText("{\"type\":\"audio_ready\"}")
            }

            try {
                while (running.get() && !session.isClosed && !socket.isClosed) {
                    val frame = readWsFrame(input) ?: break
                    when (frame.opcode) {
                        0x01 -> { // Text JSON (touch, action, resolution, ping)
                            val text = String(frame.payload, Charsets.UTF_8)
                            try {
                                val json = JSONObject(text)
                                when (json.optString("type")) {
                                    "touch" -> {
                                        val arr = json.optJSONArray("contacts") ?: JSONArray()
                                        val list = mutableListOf<AirPlayContact>()
                                        for (i in 0 until arr.length()) {
                                            val c = arr.getJSONObject(i)
                                            list.add(
                                                AirPlayContact(
                                                    id = c.optInt("id", i),
                                                    x = c.optDouble("x", 0.0),
                                                    y = c.optDouble("y", 0.0),
                                                    down = c.optBoolean("down", false),
                                                )
                                            )
                                        }
                                        onTouch(list)
                                    }
                                    "action" -> {
                                        val action = json.optString("action")
                                        if (action.isNotEmpty()) onAction(action)
                                    }
                                    "resolution" -> {
                                        val w = json.optInt("width", 0)
                                        val h = json.optInt("height", 0)
                                        if (w in 400..3840 && h in 300..2160) {
                                            onResolution(w, h)
                                        }
                                    }
                                    "ping" -> {
                                        session.sendText("{\"type\":\"pong\"}")
                                    }
                                    "requestKeyFrame" -> {
                                        CarPlayVideoBridge.requestKeyFrame()
                                    }
                                    "log" -> {
                                        Log.i("WebClientLog", json.optString("msg"))
                                    }
                                }
                            } catch (e: Exception) {
                                Log.w(TAG, "Error processing WS text frame", e)
                            }
                        }
                        0x02 -> { // Binary (Web microphone 16kHz PCM data)
                            if (frame.payload.isNotEmpty()) {
                                onMicData?.invoke(frame.payload)
                            }
                        }
                        0x08 -> { // Close frame
                            break
                        }
                        0x09 -> { // Ping frame -> reply pong
                            session.sendPong(frame.payload)
                        }
                    }
                }
            } catch (_: SocketException) {
            } catch (e: Exception) {
                Log.d(TAG, "WebSocket session exception", e)
            } finally {
                session.close()
                wsClients.remove(session)
                wsAudioClients.remove(session)
                notifyClientsChanged()
            }
        } catch (e: Exception) {
            Log.w(TAG, "WebSocket upgrade failed", e)
            try { socket.close() } catch (_: Exception) {}
        }
    }

    private data class WsFrame(val fin: Boolean, val opcode: Int, val payload: ByteArray)

    private fun readWsFrame(input: InputStream): WsFrame? {
        val b0 = input.read()
        if (b0 < 0) return null
        val fin = (b0 and 0x80) != 0
        val opcode = b0 and 0x0F

        val b1 = input.read()
        if (b1 < 0) return null
        val masked = (b1 and 0x80) != 0
        var len = (b1 and 0x7F).toLong()

        if (len == 126L) {
            val ch1 = input.read()
            val ch2 = input.read()
            if ((ch1 or ch2) < 0) return null
            len = ((ch1 shl 8) or ch2).toLong()
        } else if (len == 127L) {
            len = 0L
            for (i in 0 until 8) {
                val ch = input.read()
                if (ch < 0) return null
                len = (len shl 8) or ch.toLong()
            }
        }

        if (len < 0 || len > 10 * 1024 * 1024) return null

        val mask = ByteArray(4)
        if (masked) {
            var r = 0
            while (r < 4) {
                val n = input.read(mask, r, 4 - r)
                if (n < 0) return null
                r += n
            }
        }

        val payload = ByteArray(len.toInt())
        var r = 0
        while (r < len) {
            val n = input.read(payload, r, len.toInt() - r)
            if (n < 0) return null
            r += n
        }

        if (masked) {
            for (i in payload.indices) {
                payload[i] = (payload[i].toInt() xor mask[i % 4].toInt()).toByte()
            }
        }

        return WsFrame(fin, opcode, payload)
    }

    class WsSession(
        val socket: Socket,
        val output: OutputStream,
    ) {
        private val writeLock = Any()
        @Volatile var isClosed = false

        fun sendText(text: String): Boolean {
            val bytes = text.toByteArray(Charsets.UTF_8)
            return sendFrame(0x01, bytes, 0, bytes.size)
        }

        fun sendBinary(data: ByteArray, offset: Int = 0, length: Int = data.size): Boolean {
            return sendFrame(0x02, data, offset, length)
        }

        fun sendMultiplexBinary(channel: Byte, flag: Byte, data: ByteArray, offset: Int = 0, length: Int = data.size): Boolean {
            val totalLen = length + 4
            if (isClosed || socket.isClosed) return false
            return synchronized(writeLock) {
                try {
                    val header = ByteArray(14)
                    header[0] = 0x82.toByte() // FIN + Binary frame opcode
                    var hLen: Int
                    if (totalLen <= 125) {
                        header[1] = totalLen.toByte()
                        hLen = 2
                    } else if (totalLen <= 65535) {
                        header[1] = 126.toByte()
                        header[2] = ((totalLen shr 8) and 0xFF).toByte()
                        header[3] = (totalLen and 0xFF).toByte()
                        hLen = 4
                    } else {
                        header[1] = 127.toByte()
                        val l = totalLen.toLong()
                        for (i in 7 downTo 0) {
                            header[2 + (7 - i)] = ((l shr (i * 8)) and 0xFF).toByte()
                        }
                        hLen = 10
                    }
                    header[hLen] = channel
                    header[hLen + 1] = flag
                    header[hLen + 2] = 0
                    header[hLen + 3] = 0
                    hLen += 4

                    output.write(header, 0, hLen)
                    output.write(data, offset, length)
                    output.flush()
                    true
                } catch (_: Exception) {
                    close()
                    false
                }
            }
        }

        fun sendPong(payload: ByteArray): Boolean {
            return sendFrame(0x0A, payload, 0, payload.size)
        }

        private fun sendFrame(opcode: Int, data: ByteArray, offset: Int, length: Int): Boolean {
            if (isClosed || socket.isClosed) return false
            return synchronized(writeLock) {
                try {
                    output.write(0x80 or (opcode and 0x0F))
                    if (length <= 125) {
                        output.write(length)
                    } else if (length <= 65535) {
                        output.write(126)
                        output.write((length shr 8) and 0xFF)
                        output.write(length and 0xFF)
                    } else {
                        output.write(127)
                        val l = length.toLong()
                        for (i in 7 downTo 0) {
                            output.write(((l shr (i * 8)) and 0xFF).toInt())
                        }
                    }
                    output.write(data, offset, length)
                    output.flush()
                    true
                } catch (_: Exception) {
                    close()
                    false
                }
            }
        }

        fun close() {
            if (isClosed) return
            isClosed = true
            try {
                sendFrame(0x08, ByteArray(0), 0, 0)
            } catch (_: Exception) {}
            try { socket.close() } catch (_: Exception) {}
        }
    }

    class MjpegSession(
        val socket: Socket,
        val output: OutputStream,
    ) {
        private val frameSlot = java.util.concurrent.atomic.AtomicReference<ByteArray?>(null)
        @Volatile var isClosed = false
        private val lock = Object()

        fun offerFrame(jpeg: ByteArray): Boolean {
            if (isClosed || socket.isClosed) return false
            frameSlot.set(jpeg)
            synchronized(lock) {
                lock.notify()
            }
            return true
        }

        fun runLoop(running: AtomicBoolean, onDisconnect: () -> Unit) {
            val crlf = "\r\n".toByteArray(Charsets.US_ASCII)
            try {
                while (running.get() && !isClosed && !socket.isClosed) {
                    var frame: ByteArray? = null
                    synchronized(lock) {
                        while (running.get() && !isClosed && !socket.isClosed && frameSlot.get() == null) {
                            lock.wait(1000)
                        }
                        frame = frameSlot.getAndSet(null)
                    }
                    if (!running.get() || isClosed || socket.isClosed) break
                    if (frame != null) {
                        val f = frame!!
                        val header = ("--frame\r\n" +
                            "Content-Type: image/jpeg\r\n" +
                            "Content-Length: ${f.size}\r\n\r\n").toByteArray(Charsets.US_ASCII)
                        output.write(header)
                        output.write(f)
                        output.write(crlf)
                        output.flush()
                    }
                }
            } catch (_: Exception) {
            } finally {
                close()
                onDisconnect()
            }
        }

        fun close() {
            isClosed = true
            synchronized(lock) {
                lock.notifyAll()
            }
            try { socket.close() } catch (_: Exception) {}
        }
    }

    private fun readAsciiLine(input: InputStream): String? {
        val out = ByteArrayOutputStream(64)
        while (true) {
            val b = input.read()
            if (b < 0) {
                return if (out.size() > 0) out.toString("US-ASCII") else null
            }
            if (b == '\n'.code) {
                val bytes = out.toByteArray()
                val len = if (bytes.isNotEmpty() && bytes.last() == '\r'.code.toByte()) bytes.size - 1 else bytes.size
                return String(bytes, 0, len, Charsets.US_ASCII)
            }
            out.write(b)
        }
    }

    private fun readBody(input: InputStream, length: Int): String {
        if (length <= 0) return ""
        val buf = ByteArray(length)
        var readTotal = 0
        while (readTotal < length) {
            val r = input.read(buf, readTotal, length - readTotal)
            if (r < 0) break
            readTotal += r
        }
        return String(buf, 0, readTotal, Charsets.UTF_8)
    }

    private fun sendCors(output: OutputStream, status: Int, statusText: String) {
        val resp = (
            "HTTP/1.1 $status $statusText\r\n" +
            "Access-Control-Allow-Origin: *\r\n" +
            "Access-Control-Allow-Methods: GET, POST, OPTIONS\r\n" +
            "Access-Control-Allow-Headers: Content-Type\r\n" +
            "Content-Length: 0\r\n" +
            "Connection: close\r\n\r\n"
        ).toByteArray(Charsets.US_ASCII)
        output.write(resp)
        output.flush()
    }

    private fun sendResponse(output: OutputStream, status: Int, statusText: String, contentType: String, data: ByteArray) {
        val resp = (
            "HTTP/1.1 $status $statusText\r\n" +
            "Content-Type: $contentType\r\n" +
            "Content-Length: ${data.size}\r\n" +
            "Access-Control-Allow-Origin: *\r\n" +
            "Connection: close\r\n\r\n"
        ).toByteArray(Charsets.US_ASCII)
        output.write(resp)
        output.write(data)
        output.flush()
    }

    private fun createEmptyJpeg(): ByteArray = EMPTY_JPEG

    companion object {
        private const val TAG = "CarPlayWebServer"

        private val EMPTY_JPEG = byteArrayOf(
            0xFF.toByte(), 0xD8.toByte(), 0xFF.toByte(), 0xDB.toByte(), 0x00, 0x43, 0x00,
            0x08, 0x06, 0x06, 0x07, 0x06, 0x05, 0x08, 0x07, 0x07, 0x07, 0x09, 0x09, 0x08,
            0x0A, 0x0C, 0x14, 0x0D, 0x0C, 0x0B, 0x0B, 0x0C, 0x19, 0x12, 0x13, 0x0F, 0x14,
            0x1D, 0x1A, 0x1F, 0x1E, 0x1D, 0x1A, 0x1C, 0x1C, 0x20, 0x24, 0x2E, 0x27, 0x20,
            0x22, 0x2C, 0x23, 0x1C, 0x1C, 0x28, 0x37, 0x29, 0x2C, 0x30, 0x31, 0x34, 0x34,
            0x34, 0x1F, 0x27, 0x39, 0x3D, 0x38, 0x32, 0x3C, 0x2E, 0x33, 0x34, 0x32,
            0xFF.toByte(), 0xC0.toByte(), 0x00, 0x0B, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00,
            0xFF.toByte(), 0xC4.toByte(), 0x00, 0x1F, 0x00, 0x00, 0x01, 0x05, 0x01, 0x01, 0x01, 0x01, 0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
            0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B,
            0xFF.toByte(), 0xDA.toByte(), 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00,
            0xBF.toByte(), 0x80.toByte(),
            0xFF.toByte(), 0xD9.toByte()
        )

        fun createWavHeader(sampleRate: Int, channels: Int): ByteArray {
            val totalDataLen = 0x7FFFF000 // Infinite stream marker
            val totalAudioLen = totalDataLen + 36
            val byteRate = sampleRate * channels * 2
            val header = ByteArray(44)
            header[0] = 'R'.code.toByte()
            header[1] = 'I'.code.toByte()
            header[2] = 'F'.code.toByte()
            header[3] = 'F'.code.toByte()
            header[4] = (totalAudioLen and 0xff).toByte()
            header[5] = ((totalAudioLen shr 8) and 0xff).toByte()
            header[6] = ((totalAudioLen shr 16) and 0xff).toByte()
            header[7] = ((totalAudioLen shr 24) and 0xff).toByte()
            header[8] = 'W'.code.toByte()
            header[9] = 'A'.code.toByte()
            header[10] = 'V'.code.toByte()
            header[11] = 'E'.code.toByte()
            header[12] = 'f'.code.toByte()
            header[13] = 'm'.code.toByte()
            header[14] = 't'.code.toByte()
            header[15] = ' '.code.toByte()
            header[16] = 16 // 4 bytes: size of 'fmt ' chunk
            header[17] = 0
            header[18] = 0
            header[19] = 0
            header[20] = 1 // format = 1 (PCM)
            header[21] = 0
            header[22] = channels.toByte()
            header[23] = 0
            header[24] = (sampleRate and 0xff).toByte()
            header[25] = ((sampleRate shr 8) and 0xff).toByte()
            header[26] = ((sampleRate shr 16) and 0xff).toByte()
            header[27] = ((sampleRate shr 24) and 0xff).toByte()
            header[28] = (byteRate and 0xff).toByte()
            header[29] = ((byteRate shr 8) and 0xff).toByte()
            header[30] = ((byteRate shr 16) and 0xff).toByte()
            header[31] = ((byteRate shr 24) and 0xff).toByte()
            header[32] = (channels * 2).toByte() // block align
            header[33] = 0
            header[34] = 16 // bits per sample
            header[35] = 0
            header[36] = 'd'.code.toByte()
            header[37] = 'a'.code.toByte()
            header[38] = 't'.code.toByte()
            header[39] = 'a'.code.toByte()
            header[40] = (totalDataLen and 0xff).toByte()
            header[41] = ((totalDataLen shr 8) and 0xff).toByte()
            header[42] = ((totalDataLen shr 16) and 0xff).toByte()
            header[43] = ((totalDataLen shr 24) and 0xff).toByte()
            return header
        }
    }
}
