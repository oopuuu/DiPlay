function Texture(gl) {
    this.gl = gl;
    this.texture = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, this.texture);

    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);

    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    this.width = 0;
    this.height = 0;
}

Texture.prototype.bind = function (n, program, name) {
    var gl = this.gl;
    gl.activeTexture([gl.TEXTURE0, gl.TEXTURE1, gl.TEXTURE2][n]);
    gl.bindTexture(gl.TEXTURE_2D, this.texture);
    gl.uniform1i(gl.getUniformLocation(program, name), n);
};

Texture.prototype.fill = function (width, height, data) {
    var gl = this.gl;
    gl.bindTexture(gl.TEXTURE_2D, this.texture);
    if (this.width !== width || this.height !== height) {
        this.width = width;
        this.height = height;
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.LUMINANCE, width, height, 0, gl.LUMINANCE, gl.UNSIGNED_BYTE, data);
    } else {
        gl.texSubImage2D(gl.TEXTURE_2D, 0, 0, 0, width, height, gl.LUMINANCE, gl.UNSIGNED_BYTE, data);
    }
};

function WebGLPlayer(canvas, options) {
    this.canvas = canvas;
    var glOpts = {
        alpha: false,
        depth: false,
        stencil: false,
        antialias: false,
        preserveDrawingBuffer: false,
        powerPreference: "high-performance",
        desynchronized: true
    };
    if (options) {
        for (var k in options) {
            glOpts[k] = options[k];
        }
    }
    // Prefer WebGL 2.0 for UNPACK_ROW_LENGTH zero-copy strided DMA texture upload (60 FPS)
    this.gl = canvas.getContext("webgl2", glOpts) || canvas.getContext("webgl", glOpts) || canvas.getContext("experimental-webgl", glOpts);
    this.isWebGL2 = (typeof WebGL2RenderingContext !== "undefined" && this.gl instanceof WebGL2RenderingContext);
    this.initGL(options);
}

WebGLPlayer.prototype.initGL = function (options) {
    if (!this.gl) {
        console.log("[ER] WebGL not supported.");
        return;
    }

    var gl = this.gl;
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    var program = gl.createProgram();
    var vertexShaderSource = [
        "attribute highp vec4 aVertexPosition;",
        "attribute vec2 aTextureCoord;",
        "varying highp vec2 vTextureCoord;",
        "void main(void) {",
        " gl_Position = aVertexPosition;",
        " vTextureCoord = aTextureCoord;",
        "}"
    ].join("\n");
    var vertexShader = gl.createShader(gl.VERTEX_SHADER);
    gl.shaderSource(vertexShader, vertexShaderSource);
    gl.compileShader(vertexShader);
    var fragmentShaderSource = [
        "precision highp float;",
        "varying lowp vec2 vTextureCoord;",
        "uniform sampler2D YTexture;",
        "uniform sampler2D UTexture;",
        "uniform sampler2D VTexture;",
        "const mat4 YUV2RGB = mat4",
        "(",
        " 1.1643828125, 0, 1.59602734375, -.87078515625,",
        " 1.1643828125, -.39176171875, -.81296875, .52959375,",
        " 1.1643828125, 2.017234375, 0, -1.081390625,",
        " 0, 0, 0, 1",
        ");",
        "void main(void) {",
        " gl_FragColor = vec4( texture2D(YTexture, vTextureCoord).x, texture2D(UTexture, vTextureCoord).x, texture2D(VTexture, vTextureCoord).x, 1) * YUV2RGB;",
        "}"
    ].join("\n");

    var fragmentShader = gl.createShader(gl.FRAGMENT_SHADER);
    gl.shaderSource(fragmentShader, fragmentShaderSource);
    gl.compileShader(fragmentShader);
    gl.attachShader(program, vertexShader);
    gl.attachShader(program, fragmentShader);
    gl.linkProgram(program);
    gl.useProgram(program);
    if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
        console.log("[ER] Shader link failed.");
    }
    var vertexPositionAttribute = gl.getAttribLocation(program, "aVertexPosition");
    gl.enableVertexAttribArray(vertexPositionAttribute);
    var textureCoordAttribute = gl.getAttribLocation(program, "aTextureCoord");
    gl.enableVertexAttribArray(textureCoordAttribute);

    var verticesBuffer = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, verticesBuffer);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([1.0, 1.0, 0.0, -1.0, 1.0, 0.0, 1.0, -1.0, 0.0, -1.0, -1.0, 0.0]), gl.STATIC_DRAW);
    gl.vertexAttribPointer(vertexPositionAttribute, 3, gl.FLOAT, false, 0, 0);
    var texCoordBuffer = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, texCoordBuffer);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([1.0, 0.0, 0.0, 0.0, 1.0, 1.0, 0.0, 1.0]), gl.STATIC_DRAW);
    gl.vertexAttribPointer(textureCoordAttribute, 2, gl.FLOAT, false, 0, 0);

    gl.y = new Texture(gl);
    gl.u = new Texture(gl);
    gl.v = new Texture(gl);
    gl.y.bind(0, program, "YTexture");
    gl.u.bind(1, program, "UTexture");
    gl.v.bind(2, program, "VTexture");
}

WebGLPlayer.prototype.renderFrame = function (videoFrame, width, height, uOffset, vOffset) {
    if (!this.gl) {
        console.log("[ER] Render frame failed due to WebGL not supported.");
        return;
    }

    var gl = this.gl;
    if (this.vpW !== width || this.vpH !== height) {
        this.vpW = width;
        this.vpH = height;
        gl.viewport(0, 0, width, height);
    }

    gl.y.fill(width, height, videoFrame.subarray(0, uOffset));
    gl.u.fill(width >> 1, height >> 1, videoFrame.subarray(uOffset, uOffset + vOffset));
    gl.v.fill(width >> 1, height >> 1, videoFrame.subarray(uOffset + vOffset, videoFrame.length));

    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
};

WebGLPlayer.prototype.renderDirectYUV = function (u8Heap, addr_y, addr_u, addr_v, width, height, stride_y, stride_u, stride_v) {
    if (!this.gl) return;
    var gl = this.gl;
    if (this.vpW !== width || this.vpH !== height) {
        this.vpW = width;
        this.vpH = height;
        gl.viewport(0, 0, width, height);
    }
    var yLen = width * height;
    var uvWidth = width >> 1;
    var uvHeight = height >> 1;
    var uvLen = uvWidth * uvHeight;

    if (this.isWebGL2) {
        // WebGL 2.0 Native Hardware DMA: ZERO CPU JavaScript memory copies, GPU driver handles strided upload directly!
        gl.pixelStorei(gl.UNPACK_ROW_LENGTH, stride_y);
        gl.y.fill(width, height, u8Heap.subarray(addr_y, addr_y + stride_y * height));
        gl.pixelStorei(gl.UNPACK_ROW_LENGTH, stride_u);
        gl.u.fill(uvWidth, uvHeight, u8Heap.subarray(addr_u, addr_u + stride_u * uvHeight));
        gl.pixelStorei(gl.UNPACK_ROW_LENGTH, stride_v);
        gl.v.fill(uvWidth, uvHeight, u8Heap.subarray(addr_v, addr_v + stride_v * uvHeight));
        gl.pixelStorei(gl.UNPACK_ROW_LENGTH, 0); // Restore default
    } else if (stride_y === width && stride_u === uvWidth && stride_v === uvWidth) {
        // WebGL 1.0 zero-copy direct contiguous upload
        gl.y.fill(width, height, u8Heap.subarray(addr_y, addr_y + yLen));
        gl.u.fill(uvWidth, uvHeight, u8Heap.subarray(addr_u, addr_u + uvLen));
        gl.v.fill(uvWidth, uvHeight, u8Heap.subarray(addr_v, addr_v + uvLen));
    } else {
        // Fallback for WebGL 1.0 with padded strides
        if (!this.bufY || this.bufY.length !== yLen) {
            this.bufY = new Uint8Array(yLen);
            this.bufU = new Uint8Array(uvLen);
            this.bufV = new Uint8Array(uvLen);
        }
        var pos = 0;
        for (var i = 0; i < height; i++) {
            var src = addr_y + i * stride_y;
            this.bufY.set(u8Heap.subarray(src, src + width), pos);
            pos += width;
        }
        pos = 0;
        for (var i = 0; i < uvHeight; i++) {
            var src = addr_u + i * stride_u;
            this.bufU.set(u8Heap.subarray(src, src + uvWidth), pos);
            pos += uvWidth;
        }
        pos = 0;
        for (var i = 0; i < uvHeight; i++) {
            var src = addr_v + i * stride_v;
            this.bufV.set(u8Heap.subarray(src, src + uvWidth), pos);
            pos += uvWidth;
        }
        gl.y.fill(width, height, this.bufY);
        gl.u.fill(uvWidth, uvHeight, this.bufU);
        gl.v.fill(uvWidth, uvHeight, this.bufV);
    }
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
};

WebGLPlayer.prototype.fullscreen = function () {
	  var canvas = this.canvas;
    if (canvas.RequestFullScreen) {
        canvas.RequestFullScreen();
    } else if (canvas.webkitRequestFullScreen) {
        canvas.webkitRequestFullScreen();
    } else if (canvas.mozRequestFullScreen) {
        canvas.mozRequestFullScreen();
    } else if (canvas.msRequestFullscreen) {
        canvas.msRequestFullscreen();
    } else {
        alert("This browser doesn't supporter fullscreen");
    }
};

WebGLPlayer.prototype.exitfullscreen = function (){
    if (document.exitFullscreen) {
        document.exitFullscreen();
    } else if (document.webkitExitFullscreen) {
        document.webkitExitFullscreen();
    } else if (document.mozCancelFullScreen) {
        document.mozCancelFullScreen();
    } else if (document.msExitFullscreen) {
        document.msExitFullscreen();
    } else {
        alert("Exit fullscreen doesn't work");
    }
}
