package com.shilapi.xcertplay.media

import java.util.concurrent.ConcurrentLinkedQueue

/**
 * Global audio bridge between CarPlay media subsystem (in :shared)
 * and Web Remote streaming server (in :common).
 */
object CarPlayAudioBridge {
    /**
     * Intercepts PCM audio decoded from CarPlay.
     * Return true if the audio frame was handled/consumed by the Web Remote (muting phone speaker).
     * Return false to let phone AudioTrack play it normally.
     */
    @Volatile
    var audioOutputInterceptor: ((data: ByteArray, offset: Int, length: Int, sampleRate: Int, channels: Int) -> Boolean)? = null

    /**
     * Supplies microphone PCM data captured from Web Remote (browser microphone).
     * If data is available, copies up to [length] bytes into [dest] at [offset] and returns count.
     * Returns 0 if no Web microphone data is currently queued.
     */
    @Volatile
    var micInputProvider: ((dest: ByteArray, offset: Int, length: Int) -> Int)? = null

    /** Whether Web Remote is currently streaming audio to at least one browser client. */
    @Volatile
    var isWebAudioActive: Boolean = false
}
