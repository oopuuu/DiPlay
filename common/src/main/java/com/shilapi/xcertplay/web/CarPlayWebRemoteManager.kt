package com.shilapi.xcertplay.web

import android.content.Context
import android.graphics.Bitmap
import android.net.ConnectivityManager
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.TextureView
import com.shilapi.xcertplay.AirPlayPersistence
import com.shilapi.xcertplay.airplay.AirPlayContact
import com.shilapi.xcertplay.airplay.CarPlayMediaButton
import com.shilapi.xcertplay.media.CarPlayAudioBridge
import com.shilapi.xcertplay.orchestration.CarPlayController
import java.io.ByteArrayOutputStream
import java.net.Inet4Address
import java.net.NetworkInterface
import java.util.Collections
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

/**
 * Singleton orchestrator for CarPlay browser viewing and control.
 * Coordinates the embedded HTTP server, frame capture, touch forwarding, and action mapping.
 */
object CarPlayWebRemoteManager {
    private const val TAG = "WebRemoteManager"
    private const val MIN_FRAME_INTERVAL_MS = 16L // Full 60 FPS maximum fluidity
    private const val DEFAULT_JPEG_QUALITY = 92 // High-fidelity 92% JPEG encoding for sharpest crystal text

    private var server: CarPlayWebServer? = null
    private var boundActivityContext: Context? = null
    @Volatile private var activeController: CarPlayController? = null
    @Volatile private var targetTextureView: TextureView? = null
    @Volatile var onResolutionRequested: ((Int, Int) -> Unit)? = null
    @Volatile var onWebClientConnected: (() -> Unit)? = null
    @Volatile var onAllWebClientsDisconnected: (() -> Unit)? = null
    @Volatile var currentPreferredResolution: Pair<Int, Int>? = null

    private val captureExecutor = Executors.newSingleThreadExecutor { task ->
        Thread(task, "carplay-web-frame-encoder").apply { isDaemon = true }
    }
    private val mainHandler = Handler(Looper.getMainLooper())

    @Volatile private var jpegQuality = DEFAULT_JPEG_QUALITY
    @Volatile private var lastFrameTimeMs = 0L
    @Volatile private var encodingInProgress = AtomicBoolean(false)
    private var frontBitmap: Bitmap? = null
    private var backBitmap: Bitmap? = null
    private var useFront = true
    private val frameCounter = AtomicInteger(0)
    private var lastFpsTimestamp = System.currentTimeMillis()
    @Volatile private var currentFps = 0

    // Web microphone buffer
    private val webMicLock = Any()
    private val webMicStream = ByteArrayOutputStream()

    // Keep-alive timer for still screen updates (ONLY used for legacy HTTP MJPEG clients)
    private val idleTick = object : Runnable {
        override fun run() {
            val srv = server
            if (srv != null && srv.hasStreamClients() && System.currentTimeMillis() - lastFrameTimeMs > 1000L) {
                captureFrameNow()
            }
            mainHandler.postDelayed(this, 1000L)
        }
    }

    fun init(context: Context) {
        boundActivityContext = context.applicationContext
        mainHandler.removeCallbacks(idleTick)
        mainHandler.postDelayed(idleTick, 1000L)

        // Wire audio bridge hooks between CarPlay media sink/mic and Web Remote
        CarPlayAudioBridge.audioOutputInterceptor = { data, offset, length, sampleRate, channels ->
            val srv = server
            if (srv != null && srv.hasAudioClients()) {
                srv.broadcastAudio(data, offset, length, sampleRate, channels)
                true // Mute phone speaker and stream to Web
            } else {
                false // Play through phone speaker normally
            }
        }
        CarPlayAudioBridge.micInputProvider = { dest, offset, length ->
            pollWebMicData(dest, offset, length)
        }

        // Wire video bridge hooks between CarPlay media sink and Web Remote
        com.shilapi.xcertplay.media.CarPlayVideoBridge.videoConfigListener = { _, codec, codecData ->
            server?.broadcastVideoConfig(codec, codecData)
        }
        com.shilapi.xcertplay.media.CarPlayVideoBridge.videoFrameListener = { _, isKey, naluBytes ->
            server?.broadcastVideoFrame(isKey, naluBytes)
        }
    }

    fun onWebMicDataReceived(pcmBytes: ByteArray) {
        synchronized(webMicLock) {
            if (webMicStream.size() > 48000) {
                webMicStream.reset()
            }
            webMicStream.write(pcmBytes)
        }
    }

    fun pollWebMicData(dest: ByteArray, offset: Int, length: Int): Int {
        synchronized(webMicLock) {
            val available = webMicStream.size()
            if (available <= 0) return 0
            val toRead = minOf(available, length)
            val buf = webMicStream.toByteArray()
            System.arraycopy(buf, 0, dest, offset, toRead)
            webMicStream.reset()
            if (available > toRead) {
                webMicStream.write(buf, toRead, available - toRead)
            }
            return toRead
        }
    }

    /** Attach or detach the active CarPlayController and video TextureView. */
    fun attach(controller: CarPlayController?, view: TextureView?) {
        activeController = controller
        targetTextureView = view
        Log.i(TAG, "Attached controller=$controller view=$view")
        checkServerState()
    }

    /** Called from TextureView.SurfaceTextureListener.onSurfaceTextureUpdated. */
    fun onTextureFrameUpdated() {
        val srv = server ?: return
        // Channel 0x03 JPEG capture is ONLY needed if someone is requesting legacy HTTP /stream (MJPEG).
        // Modern WebSocket clients consume the zero-copy H.264 WebCodecs hardware stream (Channel 0x02).
        if (!srv.hasStreamClients()) return

        val now = System.currentTimeMillis()
        if (now - lastFrameTimeMs < MIN_FRAME_INTERVAL_MS) return
        if (encodingInProgress.get()) return

        captureFrameNow()
    }

    fun setJpegQuality(quality: Int) {
        jpegQuality = quality.coerceIn(50, 95)
    }

    fun getJpegQuality(): Int = jpegQuality

    private fun captureFrameNow() {
        val view = targetTextureView ?: return
        if (!view.isAvailable) return

        if (!encodingInProgress.compareAndSet(false, true)) return

        val grabTask = Runnable {
            val width = view.width
            val height = view.height
            if (width <= 0 || height <= 0) {
                encodingInProgress.set(false)
                return@Runnable
            }

            try {
                if (frontBitmap == null || frontBitmap?.width != width || frontBitmap?.height != height || frontBitmap?.config != Bitmap.Config.ARGB_8888) {
                    frontBitmap?.recycle()
                    backBitmap?.recycle()
                    frontBitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
                    backBitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
                }

                val currentBmp = if (useFront) frontBitmap!! else backBitmap!!
                useFront = !useFront

                view.getBitmap(currentBmp)
                lastFrameTimeMs = System.currentTimeMillis()

                // Encode to high-quality JPEG in background with preallocated buffer
                captureExecutor.execute {
                    try {
                        val stream = ByteArrayOutputStream(128 * 1024)
                        currentBmp.compress(Bitmap.CompressFormat.JPEG, jpegQuality, stream)
                        val bytes = stream.toByteArray()
                        server?.broadcastFrame(bytes)

                        val frames = frameCounter.incrementAndGet()
                        val now = System.currentTimeMillis()
                        if (now - lastFpsTimestamp >= 1000L) {
                            currentFps = frames
                            frameCounter.set(0)
                            lastFpsTimestamp = now
                        }
                    } catch (e: Exception) {
                        Log.w(TAG, "Frame encode failed", e)
                    } finally {
                        encodingInProgress.set(false)
                    }
                }
            } catch (e: Exception) {
                Log.w(TAG, "TextureView.getBitmap failed", e)
                encodingInProgress.set(false)
            }
        }

        if (Looper.myLooper() == Looper.getMainLooper()) {
            grabTask.run()
        } else {
            mainHandler.post(grabTask)
        }
    }

    fun startServer(context: Context): Boolean {
        val enabled = AirPlayPersistence.loadWebRemoteEnabled(context)
        if (!enabled) {
            stopServer()
            return false
        }
        val port = AirPlayPersistence.loadWebRemotePort(context)
        if (server != null && server?.port == port) {
            return true
        }

        stopServer()

        val newServer = CarPlayWebServer(
            port = port,
            onTouch = { contacts -> handleWebTouch(contacts) },
            onAction = { action -> handleWebAction(action) },
            onResolution = { w, h ->
                currentPreferredResolution = Pair(w, h)
                onResolutionRequested?.invoke(w, h)
            },
            getStatus = {
                val hasSess = activeController?.hasActiveAirPlayAttachment() == true
                val view = targetTextureView
                val pref = currentPreferredResolution
                CarPlayWebServer.ServerStatus(
                    active = server?.hasStreamClients() == true,
                    hasSession = hasSess,
                    width = pref?.first ?: view?.width ?: 1280,
                    height = pref?.second ?: view?.height ?: 720,
                    fps = currentFps,
                )
            },
            onMicData = { pcm -> onWebMicDataReceived(pcm) },
            onClientCountChanged = { count ->
                if (count == 0) {
                    CarPlayAudioBridge.isWebAudioActive = false
                    com.shilapi.xcertplay.media.CarPlayVideoBridge.isPhoneRenderingSuspended = false
                    com.shilapi.xcertplay.media.CarPlayVideoBridge.requestKeyFrame()
                    mainHandler.post {
                        onAllWebClientsDisconnected?.invoke()
                    }
                } else {
                    CarPlayAudioBridge.isWebAudioActive = true
                    com.shilapi.xcertplay.media.CarPlayVideoBridge.isPhoneRenderingSuspended = true
                    mainHandler.post {
                        onWebClientConnected?.invoke()
                    }
                }
            }
        )

        val ok = newServer.start()
        if (ok) {
            server = newServer
            com.shilapi.xcertplay.media.CarPlayVideoBridge.videoConfigListener = { _, codec, codecData ->
                server?.broadcastVideoConfig(codec, codecData)
            }
            com.shilapi.xcertplay.media.CarPlayVideoBridge.videoFrameListener = { _, isKey, naluBytes ->
                server?.broadcastVideoFrame(isKey, naluBytes)
            }
            Log.i(TAG, "Web Remote server successfully started on port $port")
        }
        return ok
    }

    fun stopServer() {
        server?.stop()
        server = null
    }

    fun isServerRunning(): Boolean = server != null

    fun getServerPort(context: Context): Int = server?.port ?: AirPlayPersistence.loadWebRemotePort(context)

    fun getClientCount(): Int = server?.activeClientCount ?: 0

    private fun checkServerState() {
        val ctx = boundActivityContext ?: return
        if (AirPlayPersistence.loadWebRemoteEnabled(ctx)) {
            if (server == null) {
                startServer(ctx)
            }
        } else {
            stopServer()
        }
    }

    private fun handleWebTouch(contacts: List<AirPlayContact>) {
        val controller = activeController ?: return
        try {
            controller.sendTouch(contacts)
        } catch (e: Exception) {
            Log.w(TAG, "Error forwarding web touch to controller", e)
        }
    }

    private fun handleWebAction(action: String) {
        val controller = activeController
        when {
            action == "home" -> {
                Log.i(TAG, "Triggering CarPlay Home from Web Remote")
                controller?.sendHomeButton()
            }
            action == "siri" -> {
                Log.i(TAG, "Triggering CarPlay Siri from Web Remote")
                controller?.requestSiri()
            }
            action == "play_pause" -> {
                Log.i(TAG, "Triggering CarPlay Play/Pause from Web Remote")
                controller?.sendMediaButton(CarPlayMediaButton.PLAY_PAUSE)
            }
            action == "prev" -> {
                Log.i(TAG, "Triggering CarPlay Previous Track from Web Remote")
                controller?.sendMediaButton(CarPlayMediaButton.PREVIOUS)
            }
            action == "next" -> {
                Log.i(TAG, "Triggering CarPlay Next Track from Web Remote")
                controller?.sendMediaButton(CarPlayMediaButton.NEXT)
            }
            action.startsWith("quality_") -> {
                val q = action.removePrefix("quality_").toIntOrNull()
                if (q != null && q in 20..100) {
                    jpegQuality = q
                    Log.i(TAG, "Adjusted Web Remote JPEG quality to $q%")
                }
            }
        }
    }

    /** Retrieves all available local IPv4 addresses (Wi-Fi, Hotspot, Ethernet, etc.). */
    fun getLocalIpAddresses(): List<String> {
        val hotspotIps = mutableListOf<String>()
        val wlanIps = mutableListOf<String>()
        val otherIps = mutableListOf<String>()

        try {
            val interfaces = NetworkInterface.getNetworkInterfaces() ?: return emptyList()
            for (iface in Collections.list(interfaces)) {
                if (!iface.isUp || iface.isLoopback) continue
                val name = iface.name.lowercase()
                val isHotspotIface = name.startsWith("ap") || name.startsWith("softap") || name.startsWith("swlan")

                for (addr in Collections.list(iface.inetAddresses)) {
                    if (addr is Inet4Address && !addr.isLoopbackAddress) {
                        val host = addr.hostAddress ?: continue
                        if (!host.startsWith("127.")) {
                            if (isHotspotIface || host.startsWith("192.168.43.") || host.startsWith("192.168.49.") || host.startsWith("192.168.50.")) {
                                hotspotIps.add(host)
                            } else if (name.startsWith("wlan") || name.startsWith("eth")) {
                                wlanIps.add(host)
                            } else {
                                otherIps.add(host)
                            }
                        }
                    }
                }
            }
        } catch (e: Exception) {
            Log.w(TAG, "Failed to resolve network IP addresses", e)
        }

        val all = mutableListOf<String>()
        all.addAll(hotspotIps)
        all.addAll(wlanIps)
        all.addAll(otherIps)
        return all.distinct()
    }

    /** Returns primary access URL, e.g. "http://192.168.43.1:8088" */
    fun getPrimaryAccessUrl(context: Context): String {
        val port = getServerPort(context)
        val ips = getLocalIpAddresses()
        val primaryIp = ips.firstOrNull() ?: "127.0.0.1"
        return "http://$primaryIp:$port"
    }
}
