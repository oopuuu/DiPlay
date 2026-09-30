package com.shilapi.xcertplay.media

import com.shilapi.xcertplay.airplay.VideoCodec

/**
 * Global video bridge between CarPlay video decoder pipeline (in :shared)
 * and Web Remote streaming server (in :common).
 * Supplies direct H.264/H.265 packets to Web Remote clients
 * for true 60 FPS hardware video streaming in modern browsers via WebCodecs.
 */
object CarPlayVideoBridge {
    /**
     * Supplies video codec and parameter sets (avcC / SPS / PPS config).
     */
    @Volatile
    var videoConfigListener: ((type: Int, codec: VideoCodec, codecData: ByteArray) -> Unit)? = null

    /**
     * Supplies raw video NAL units from iPhone CarPlay.
     * [isKeyFrame]: whether this frame starts with an IDR / I-frame
     * [naluBytes]: raw AVCC length-prefixed NAL units directly from iPhone
     */
    @Volatile
    var videoFrameListener: ((type: Int, isKeyFrame: Boolean, naluBytes: ByteArray) -> Unit)? = null

    /**
     * Cached last video config (avcC) so newly connected Web clients get instant init data.
     */
    @Volatile
    var lastCodecData: ByteArray? = null

    @Volatile
    var lastCodec: VideoCodec = VideoCodec.H264

    @Volatile
    var sps: ByteArray? = null

    @Volatile
    var pps: ByteArray? = null

    /** Cached last IDR KeyFrame so newly connected Web clients get instant decode without waiting */
    @Volatile
    var lastIdrFrame: ByteArray? = null

    /** Cached latest frame */
    @Volatile
    var lastFrame: ByteArray? = null

    /**
     * Whether phone local screen decoding & rendering is suspended
     * because a Web client is actively streaming CarPlay via WebCodecs.
     */
    @Volatile
    var isPhoneRenderingSuspended: Boolean = false

    /**
     * Requests a keyframe from the CarPlay source (iPhone).
     */
    @Volatile
    var keyFrameRequester: (() -> Unit)? = null

    fun onConfig(type: Int, codec: VideoCodec, codecData: ByteArray) {
        lastCodec = codec
        lastCodecData = codecData
        if (codec == VideoCodec.H264) {
            val (parsedSps, parsedPps) = MediaCodecSupport.avcParameterSets(codecData)
            sps = parsedSps
            pps = parsedPps
        }
        videoConfigListener?.invoke(type, codec, codecData)
    }

    fun onFrame(type: Int, isKeyFrame: Boolean, naluBytes: ByteArray) {
        lastFrame = naluBytes
        if (isKeyFrame) {
            lastIdrFrame = naluBytes
        }
        videoFrameListener?.invoke(type, isKeyFrame, naluBytes)
    }

    fun requestKeyFrame() {
        keyFrameRequester?.invoke()
    }
}
