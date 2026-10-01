package com.shilapi.xcertplay.web

import android.Manifest
import android.annotation.SuppressLint
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.location.LocationManager
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import androidx.core.content.ContextCompat
import androidx.core.location.LocationManagerCompat

/**
 * Manages Android LocalOnlyHotspot (SoftAP) specifically designed for SIM-less scenarios.
 *
 * LocalOnlyHotspot does NOT require:
 * 1. SIM Card
 * 2. Mobile Cellular Data
 * 3. Carrier Entitlement / Provisioning check
 *
 * It creates a standalone high-speed Wi-Fi network (AP) on the device, allowing
 * Tesla / in-car browsers to connect directly over local IP (e.g. http://192.168.43.1:8088).
 */
object CarPlayHotspotManager {
    private const val TAG = "CarPlayHotspot"

    data class HotspotInfo(
        val isActive: Boolean = false,
        val isStarting: Boolean = false,
        val ssid: String? = null,
        val password: String? = null,
        val ipAddress: String? = null,
        val webUrl: String? = null,
        val errorMessage: String? = null
    )

    private var reservation: WifiManager.LocalOnlyHotspotReservation? = null
    private val mainHandler = Handler(Looper.getMainLooper())

    var currentInfo: HotspotInfo = HotspotInfo()
        private set

    private val listeners = mutableListOf<(HotspotInfo) -> Unit>()

    fun addListener(listener: (HotspotInfo) -> Unit) {
        if (!listeners.contains(listener)) {
            listeners.add(listener)
        }
        listener(currentInfo)
    }

    fun removeListener(listener: (HotspotInfo) -> Unit) {
        listeners.remove(listener)
    }

    private fun notifyUpdate(info: HotspotInfo) {
        currentInfo = info
        mainHandler.post {
            for (l in listeners) {
                l(info)
            }
        }
    }

    /**
     * Checks if all required permissions for LocalOnlyHotspot are granted.
     */
    fun hasRequiredPermissions(context: Context): Boolean {
        val fineLocation = ContextCompat.checkSelfPermission(
            context,
            Manifest.permission.ACCESS_FINE_LOCATION
        ) == PackageManager.PERMISSION_GRANTED

        val nearbyDevices = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            ContextCompat.checkSelfPermission(
                context,
                Manifest.permission.NEARBY_WIFI_DEVICES
            ) == PackageManager.PERMISSION_GRANTED
        } else {
            true
        }

        return fineLocation && nearbyDevices
    }

    /**
     * Required permission array for requesting via ActivityResultContracts.RequestMultiplePermissions.
     */
    fun getRequiredPermissions(): Array<String> {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            arrayOf(
                Manifest.permission.ACCESS_FINE_LOCATION,
                Manifest.permission.NEARBY_WIFI_DEVICES
            )
        } else {
            arrayOf(Manifest.permission.ACCESS_FINE_LOCATION)
        }
    }

    /**
     * Checks if location service (GPS) is turned on.
     * Some Android versions block startLocalOnlyHotspot if location mode is disabled.
     */
    fun isLocationModeEnabled(context: Context): Boolean {
        val lm = context.getSystemService(Context.LOCATION_SERVICE) as? LocationManager ?: return false
        return LocationManagerCompat.isLocationEnabled(lm)
    }

    /**
     * Starts the Local-only Hotspot.
     */
    @SuppressLint("MissingPermission")
    fun startHotspot(context: Context) {
        val appContext = context.applicationContext
        if (currentInfo.isActive && reservation != null) {
            Log.i(TAG, "Hotspot is already active")
            return
        }

        if (!hasRequiredPermissions(appContext)) {
            notifyUpdate(
                currentInfo.copy(
                    isStarting = false,
                    errorMessage = "Missing required Wi-Fi / Location permissions"
                )
            )
            return
        }

        if (!isLocationModeEnabled(appContext)) {
            notifyUpdate(
                currentInfo.copy(
                    isStarting = false,
                    errorMessage = "Location services (GPS) must be enabled in system settings"
                )
            )
            return
        }

        val wifiManager = appContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager
        if (wifiManager == null) {
            notifyUpdate(
                currentInfo.copy(
                    isStarting = false,
                    errorMessage = "WifiManager unavailable on this device"
                )
            )
            return
        }

        notifyUpdate(
            currentInfo.copy(
                isStarting = true,
                errorMessage = null
            )
        )

        try {
            wifiManager.startLocalOnlyHotspot(object : WifiManager.LocalOnlyHotspotCallback() {
                override fun onStarted(res: WifiManager.LocalOnlyHotspotReservation) {
                    super.onStarted(res)
                    reservation = res
                    Log.i(TAG, "LocalOnlyHotspot successfully started!")

                    var ssid: String? = null
                    var pass: String? = null

                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                        try {
                            val config = res.softApConfiguration
                            ssid = config.ssid
                            pass = config.passphrase
                        } catch (e: Throwable) {
                            Log.w(TAG, "Failed reading softApConfiguration", e)
                        }
                    }

                    if (ssid == null) {
                        @Suppress("DEPRECATION")
                        val config = res.wifiConfiguration
                        ssid = config?.SSID
                        pass = config?.preSharedKey
                    }

                    // Auto start WebRemote server if not already running
                    CarPlayWebRemoteManager.init(appContext)
                    CarPlayWebRemoteManager.startServer(appContext)

                    // Delay a moment for network interface assignment
                    mainHandler.postDelayed({
                        val ip = CarPlayWebRemoteManager.getLocalIpAddresses().firstOrNull() ?: "192.168.43.1"
                        val port = CarPlayWebRemoteManager.getServerPort(appContext)
                        val url = "http://$ip:$port"

                        notifyUpdate(
                            HotspotInfo(
                                isActive = true,
                                isStarting = false,
                                ssid = ssid ?: "DiPlay_Hotspot",
                                password = pass ?: "(Open / See System)",
                                ipAddress = ip,
                                webUrl = url,
                                errorMessage = null
                            )
                        )
                    }, 1000)
                }

                override fun onStopped() {
                    super.onStopped()
                    Log.i(TAG, "LocalOnlyHotspot stopped")
                    reservation = null
                    notifyUpdate(
                        HotspotInfo(
                            isActive = false,
                            isStarting = false,
                            ssid = null,
                            password = null,
                            ipAddress = null,
                            webUrl = null,
                            errorMessage = null
                        )
                    )
                }

                override fun onFailed(reason: Int) {
                    super.onFailed(reason)
                    reservation = null
                    val msg = when (reason) {
                        ERROR_NO_CHANNEL -> "Error: No Wi-Fi channel available for AP mode"
                        ERROR_GENERIC -> "Error: Generic Wi-Fi hotspot error (Try rebooting Wi-Fi)"
                        ERROR_INCOMPATIBLE_MODE -> "Error: Wi-Fi mode incompatible with Hotspot"
                        ERROR_TETHERING_DISALLOWED -> "Error: Tethering disallowed by system policy"
                        else -> "Failed to start local hotspot (Code: $reason)"
                    }
                    Log.e(TAG, "LocalOnlyHotspot start failed: $msg")
                    notifyUpdate(
                        HotspotInfo(
                            isActive = false,
                            isStarting = false,
                            errorMessage = msg
                        )
                    )
                }
            }, mainHandler)
        } catch (e: SecurityException) {
            Log.e(TAG, "SecurityException starting LocalOnlyHotspot", e)
            notifyUpdate(
                HotspotInfo(
                    isActive = false,
                    isStarting = false,
                    errorMessage = "SecurityException: Permission denied (${e.message})"
                )
            )
        } catch (e: Exception) {
            Log.e(TAG, "Exception starting LocalOnlyHotspot", e)
            notifyUpdate(
                HotspotInfo(
                    isActive = false,
                    isStarting = false,
                    errorMessage = "Failed to start: ${e.message}"
                )
            )
        }
    }

    /**
     * Stops the active Local-only Hotspot.
     */
    fun stopHotspot() {
        try {
            reservation?.close()
        } catch (e: Exception) {
            Log.w(TAG, "Error closing hotspot reservation", e)
        }
        reservation = null
        notifyUpdate(
            HotspotInfo(
                isActive = false,
                isStarting = false,
                ssid = null,
                password = null,
                ipAddress = null,
                webUrl = null,
                errorMessage = null
            )
        )
    }

    /**
     * Copies hotspot credentials to clipboard for easy input on vehicle/browser.
     */
    fun copyCredentials(context: Context): Boolean {
        val info = currentInfo
        val text = buildString {
            append("Wi-Fi SSID: ").append(info.ssid ?: "").append("\n")
            append("Password: ").append(info.password ?: "").append("\n")
            append("Tesla Browser URL: ").append(info.webUrl ?: "http://192.168.43.1:8088")
        }
        val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager ?: return false
        val clip = ClipData.newPlainText("DiPlay Hotspot Info", text)
        clipboard.setPrimaryClip(clip)
        return true
    }

    /**
     * Intent to open system wireless or tethering settings as a fallback.
     */
    fun createSystemHotspotIntent(): Intent {
        val tetherIntent = Intent("android.settings.TETHER_SETTINGS")
        return tetherIntent
    }
}
