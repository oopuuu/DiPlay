package com.shilapi.xcertplay.network

import java.net.Socket

/** Pure standard JVM TCP configuration for CarPlay peer socket. Zero Android dependency. */
internal object TcpLiveness {
    fun configure(socket: Socket, diagnostic: (String) -> Unit) {
        try {
            socket.keepAlive = true
            socket.tcpNoDelay = true
            diagnostic("TCP keepAlive & tcpNoDelay enabled for CarPlay session")
        } catch (e: Exception) {
            diagnostic("TCP config exception: ${e.message}")
        }
    }
}
