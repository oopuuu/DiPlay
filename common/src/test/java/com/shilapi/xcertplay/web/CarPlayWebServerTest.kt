package com.shilapi.xcertplay.web

import com.shilapi.xcertplay.airplay.AirPlayContact
import org.json.JSONObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.OutputStreamWriter
import java.net.HttpURLConnection
import java.net.ServerSocket
import java.net.URL

class CarPlayWebServerTest {
    private var testPort: Int = 0
    private var server: CarPlayWebServer? = null
    private val receivedTouches = mutableListOf<List<AirPlayContact>>()
    private val receivedActions = mutableListOf<String>()
    private val receivedResolutions = mutableListOf<Pair<Int, Int>>()

    @Before
    fun setUp() {
        com.shilapi.xcertplay.media.CarPlayVideoBridge.lastCodecData = null
        val ephemeral = ServerSocket(0)
        testPort = ephemeral.localPort
        ephemeral.close()

        server = CarPlayWebServer(
            port = testPort,
            onTouch = { touches -> receivedTouches.add(touches) },
            onAction = { action -> receivedActions.add(action) },
            onResolution = { w, h -> receivedResolutions.add(Pair(w, h)) },
            getStatus = {
                CarPlayWebServer.ServerStatus(
                    active = true,
                    hasSession = true,
                    width = 1920,
                    height = 1080,
                    fps = 30,
                )
            }
        ).apply {
            start()
        }
        Thread.sleep(100)
    }

    @After
    fun tearDown() {
        server?.stop()
    }

    @Test
    fun getHtmlPage() {
        val url = URL("http://127.0.0.1:$testPort/")
        val conn = url.openConnection() as HttpURLConnection
        conn.connectTimeout = 2000
        conn.readTimeout = 2000
        conn.requestMethod = "GET"

        assertEquals(200, conn.responseCode)
        val body = conn.inputStream.bufferedReader().readText()
        assertTrue(body.contains("DiPlay Web Remote"))
        assertTrue(body.contains("carplayScreen"))
    }

    @Test
    fun getStatusJson() {
        val url = URL("http://127.0.0.1:$testPort/api/status")
        val conn = url.openConnection() as HttpURLConnection
        conn.connectTimeout = 2000
        conn.readTimeout = 2000

        assertEquals(200, conn.responseCode)
        val body = conn.inputStream.bufferedReader().readText()
        val json = JSONObject(body)
        assertTrue(json.getBoolean("active"))
        assertTrue(json.getBoolean("hasSession"))
        assertEquals(1920, json.getInt("width"))
        assertEquals(1080, json.getInt("height"))
        assertEquals(30, json.getInt("fps"))
    }

    @Test
    fun postTouchApiMultiTouch() {
        val url = URL("http://127.0.0.1:$testPort/api/touch")
        val conn = url.openConnection() as HttpURLConnection
        conn.doOutput = true
        conn.requestMethod = "POST"
        conn.setRequestProperty("Content-Type", "application/json")

        val payload = """
            {"contacts":[
                {"id":0,"x":0.45,"y":0.65,"down":true},
                {"id":1,"x":0.55,"y":0.75,"down":true}
            ]}
        """.trimIndent()

        OutputStreamWriter(conn.outputStream).use { it.write(payload); it.flush() }

        assertEquals(200, conn.responseCode)
        Thread.sleep(50)
        assertEquals(1, receivedTouches.size)
        assertEquals(2, receivedTouches[0].size)
        val c0 = receivedTouches[0][0]
        val c1 = receivedTouches[0][1]
        assertEquals(0, c0.id)
        assertEquals(0.45, c0.x, 0.001)
        assertEquals(0.65, c0.y, 0.001)
        assertTrue(c0.down)
        assertEquals(1, c1.id)
        assertEquals(0.55, c1.x, 0.001)
        assertEquals(0.75, c1.y, 0.001)
        assertTrue(c1.down)
    }

    @Test
    fun postActionApi() {
        val url = URL("http://127.0.0.1:$testPort/api/action")
        val conn = url.openConnection() as HttpURLConnection
        conn.doOutput = true
        conn.requestMethod = "POST"
        conn.setRequestProperty("Content-Type", "application/json")

        val payload = """{"action":"home"}"""
        OutputStreamWriter(conn.outputStream).use { it.write(payload); it.flush() }

        assertEquals(200, conn.responseCode)
        Thread.sleep(50)
        assertEquals(listOf("home"), receivedActions)
    }

    @Test
    fun getSnapshot() {
        val url = URL("http://127.0.0.1:$testPort/snapshot")
        val conn = url.openConnection() as HttpURLConnection
        conn.connectTimeout = 2000
        conn.readTimeout = 2000

        assertEquals(200, conn.responseCode)
        assertEquals("image/jpeg", conn.contentType)
        val bytes = conn.inputStream.readBytes()
        assertTrue(bytes.isNotEmpty())
    }

    @Test
    fun postResolutionApi() {
        val url = URL("http://127.0.0.1:$testPort/api/resolution")
        val conn = url.openConnection() as HttpURLConnection
        conn.doOutput = true
        conn.requestMethod = "POST"
        conn.setRequestProperty("Content-Type", "application/json")

        val payload = """{"width":1920,"height":1080}"""
        OutputStreamWriter(conn.outputStream).use { it.write(payload); it.flush() }

        assertEquals(200, conn.responseCode)
        Thread.sleep(50)
        assertEquals(listOf(Pair(1920, 1080)), receivedResolutions)
    }

    @Test
    fun postMicApi() {
        var receivedMicData: ByteArray? = null
        server?.onMicData = { bytes -> receivedMicData = bytes }

        val url = URL("http://127.0.0.1:$testPort/api/mic")
        val conn = url.openConnection() as HttpURLConnection
        conn.doOutput = true
        conn.requestMethod = "POST"
        conn.setRequestProperty("Content-Type", "application/octet-stream")

        val pcm = byteArrayOf(1, 2, 3, 4, 5)
        conn.outputStream.use { it.write(pcm); it.flush() }

        assertEquals(200, conn.responseCode)
        Thread.sleep(50)
        assertTrue(receivedMicData?.contentEquals(pcm) == true)
    }

    @Test
    fun getAudioStreamWavHeader() {
        val header = CarPlayWebServer.createWavHeader(44100, 2)
        assertEquals(44, header.size)
        assertEquals("RIFF", String(header, 0, 4, Charsets.US_ASCII))
        assertEquals("WAVE", String(header, 8, 4, Charsets.US_ASCII))
    }

    @Test
    fun webSocketHandshakeAndTouchAudio() {
        val socket = java.net.Socket("127.0.0.1", testPort)
        val out = socket.getOutputStream()
        val inp = socket.getInputStream()

        // 1. Send WebSocket Upgrade Request
        val request = (
            "GET /ws HTTP/1.1\r\n" +
            "Host: 127.0.0.1:$testPort\r\n" +
            "Upgrade: websocket\r\n" +
            "Connection: Upgrade\r\n" +
            "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
            "Sec-WebSocket-Version: 13\r\n\r\n"
        ).toByteArray(Charsets.US_ASCII)
        out.write(request)
        out.flush()

        // Read handshake response line by line without BufferedReader over-reading binary socket frames
        fun readLineAscii(): String {
            val sb = StringBuilder()
            while (true) {
                val b = inp.read()
                if (b == -1 || b == '\n'.code) break
                if (b != '\r'.code) sb.append(b.toChar())
            }
            return sb.toString()
        }

        val line1 = readLineAscii()
        assertTrue(line1.contains("101 Switching Protocols"))

        // Read rest of headers until blank line
        while (true) {
            val h = readLineAscii()
            if (h.isEmpty()) break
        }

        Thread.sleep(50)
        assertTrue(server?.hasWsClients() == true)

        fun readFull(bytes: ByteArray) {
            var read = 0
            while (read < bytes.size) {
                val n = inp.read(bytes, read, bytes.size - read)
                if (n < 0) break
                read += n
            }
        }

        fun readFrame(): Pair<Int, ByteArray> {
            val b0 = inp.read()
            val b1 = inp.read()
            val opcode = b0 and 0x0F
            var len = b1 and 0x7F
            if (len == 126) {
                len = (inp.read() shl 8) or inp.read()
            }
            val payload = ByteArray(len)
            readFull(payload)
            return opcode to payload
        }

        // Read initial greeting / status frame (0x81 Text frame)
        val (helloOp, helloBytes) = readFrame()
        assertEquals(0x01, helloOp)
        val helloJson = JSONObject(String(helloBytes, Charsets.UTF_8))
        assertEquals("hello", helloJson.getString("type"))

        // 2. Client sends masked touch frame (Slot 0 and Slot 1)
        val payload = """
            {"type":"touch","contacts":[{"id":0,"x":0.2,"y":0.3,"down":true},{"id":1,"x":0.8,"y":0.9,"down":true}]}
        """.trimIndent().toByteArray(Charsets.UTF_8)

        // Opcode 0x81 (FIN + Text), MASK = true
        out.write(0x81)
        out.write(0x80 or payload.size)
        val mask = byteArrayOf(0x11, 0x22, 0x33, 0x44)
        out.write(mask)
        val maskedPayload = ByteArray(payload.size) { i -> (payload[i].toInt() xor mask[i % 4].toInt()).toByte() }
        out.write(maskedPayload)
        out.flush()

        Thread.sleep(50)
        assertEquals(1, receivedTouches.size)
        assertEquals(2, receivedTouches[0].size)
        assertEquals(0.2, receivedTouches[0][0].x, 0.001)
        assertEquals(0.8, receivedTouches[0][1].x, 0.001)

        // 3. Server broadcasts PCM audio, client receives Opcode 0x82 with Channel 0x00
        val testPcm = byteArrayOf(10, 20, 30, 40, 50, 60)
        server?.broadcastAudio(testPcm, 0, testPcm.size)

        val (audioOp, recvAudio) = readFrame()
        assertEquals(0x02, audioOp) // Binary frame
        assertEquals(testPcm.size + 4, recvAudio.size)
        assertEquals(0x00.toByte(), recvAudio[0]) // Channel 0x00: Audio
        val audioData = recvAudio.copyOfRange(4, recvAudio.size)
        assertTrue(audioData.contentEquals(testPcm))

        // 4. Server broadcasts H.264 Video Frame, client receives Opcode 0x82 with Channel 0x02
        val testNalu = byteArrayOf(0, 0, 0, 1, 0x65, 1, 2, 3)
        server?.broadcastVideoFrame(isKeyFrame = true, naluBytes = testNalu)

        val (videoOp, recvVideo) = readFrame()
        assertEquals(0x02, videoOp) // Binary frame
        assertEquals(testNalu.size + 4, recvVideo.size)
        assertEquals(0x02.toByte(), recvVideo[0]) // Channel 0x02: Video Frame
        assertEquals(0x01.toByte(), recvVideo[1]) // Flag 0x01: Keyframe
        val videoData = recvVideo.copyOfRange(4, recvVideo.size)
        assertTrue(videoData.contentEquals(testNalu))

        socket.close()
    }
}
