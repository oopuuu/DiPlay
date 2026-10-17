#include <jni.h>
#include <openssl/evp.h>
#include <string.h>
#include <stdlib.h>

/*
 * C-accelerated ChaCha20-Poly1305 AEAD decryption for CarPlay AirPlay video & audio.
 * Leverages OpenSSL 3.0 ARMv8 NEON assembly implementation for extreme low latency.
 */

JNIEXPORT jbyteArray JNICALL Java_com_shilapi_xcertplay_airplay_AirPlayCrypto_nativeChachaOpen(
    JNIEnv *env, jclass clazz,
    jbyteArray keyArr,
    jbyteArray nonceArr,
    jbyteArray ctArr,
    jbyteArray aadArr)
{
    if (!keyArr || !nonceArr || !ctArr) {
        return NULL;
    }

    jsize keyLen = (*env)->GetArrayLength(env, keyArr);
    jsize nonceLen = (*env)->GetArrayLength(env, nonceArr);
    jsize ctLen = (*env)->GetArrayLength(env, ctArr);

    if (keyLen != 32 || nonceLen != 12 || ctLen < 16) {
        return NULL;
    }

    jsize dataLen = ctLen - 16;
    jsize aadLen = aadArr ? (*env)->GetArrayLength(env, aadArr) : 0;

    jbyte *key = (*env)->GetPrimitiveArrayCritical(env, keyArr, NULL);
    jbyte *nonce = (*env)->GetPrimitiveArrayCritical(env, nonceArr, NULL);
    jbyte *ct = (*env)->GetPrimitiveArrayCritical(env, ctArr, NULL);
    jbyte *aad = aadLen > 0 ? (*env)->GetPrimitiveArrayCritical(env, aadArr, NULL) : NULL;

    jbyteArray result = NULL;
    EVP_CIPHER_CTX *ctx = EVP_CIPHER_CTX_new();

    if (ctx && key && nonce && ct) {
        int outl = 0;
        int finalLen = 0;

        // Tag is located at the last 16 bytes of ciphertext
        const unsigned char *tag = (const unsigned char *)(ct + dataLen);

        if (EVP_DecryptInit_ex(ctx, EVP_chacha20_poly1305(), NULL, NULL, NULL) &&
            EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_SET_IVLEN, 12, NULL) &&
            EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_SET_TAG, 16, (void *)tag) &&
            EVP_DecryptInit_ex(ctx, NULL, NULL, (const unsigned char *)key, (const unsigned char *)nonce))
        {
            if (aadLen > 0 && aad) {
                EVP_DecryptUpdate(ctx, NULL, &outl, (const unsigned char *)aad, (int)aadLen);
            }

            // Allocate JVM output byte array
            result = (*env)->NewByteArray(env, dataLen);
            if (result) {
                jbyte *outBuf = (*env)->GetPrimitiveArrayCritical(env, result, NULL);
                if (outBuf) {
                    if (EVP_DecryptUpdate(ctx, (unsigned char *)outBuf, &outl, (const unsigned char *)ct, (int)dataLen) &&
                        EVP_DecryptFinal_ex(ctx, (unsigned char *)(outBuf + outl), &finalLen))
                    {
                        // Success!
                        (*env)->ReleasePrimitiveArrayCritical(env, result, outBuf, 0);
                    } else {
                        // Verification or auth failed
                        (*env)->ReleasePrimitiveArrayCritical(env, result, outBuf, JNI_ABORT);
                        result = NULL;
                    }
                }
            }
        }
    }

    if (ctx) {
        EVP_CIPHER_CTX_free(ctx);
    }

    if (aad && aadArr) (*env)->ReleasePrimitiveArrayCritical(env, aadArr, aad, JNI_ABORT);
    if (ct) (*env)->ReleasePrimitiveArrayCritical(env, ctArr, ct, JNI_ABORT);
    if (nonce) (*env)->ReleasePrimitiveArrayCritical(env, nonceArr, nonce, JNI_ABORT);
    if (key) (*env)->ReleasePrimitiveArrayCritical(env, keyArr, key, JNI_ABORT);

    return result;
}

JNIEXPORT jbyteArray JNICALL Java_com_shilapi_xcertplay_airplay_AirPlayCrypto_nativeChachaSeal(
    JNIEnv *env, jclass clazz,
    jbyteArray keyArr,
    jbyteArray nonceArr,
    jbyteArray ptArr,
    jbyteArray aadArr)
{
    if (!keyArr || !nonceArr || !ptArr) {
        return NULL;
    }

    jsize keyLen = (*env)->GetArrayLength(env, keyArr);
    jsize nonceLen = (*env)->GetArrayLength(env, nonceArr);
    jsize ptLen = (*env)->GetArrayLength(env, ptArr);

    if (keyLen != 32 || nonceLen != 12) {
        return NULL;
    }

    jsize aadLen = aadArr ? (*env)->GetArrayLength(env, aadArr) : 0;

    jbyte *key = (*env)->GetPrimitiveArrayCritical(env, keyArr, NULL);
    jbyte *nonce = (*env)->GetPrimitiveArrayCritical(env, nonceArr, NULL);
    jbyte *pt = (*env)->GetPrimitiveArrayCritical(env, ptArr, NULL);
    jbyte *aad = aadLen > 0 ? (*env)->GetPrimitiveArrayCritical(env, aadArr, NULL) : NULL;

    jbyteArray result = NULL;
    EVP_CIPHER_CTX *ctx = EVP_CIPHER_CTX_new();

    if (ctx && key && nonce && pt) {
        int outl = 0;
        int finalLen = 0;

        if (EVP_EncryptInit_ex(ctx, EVP_chacha20_poly1305(), NULL, NULL, NULL) &&
            EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_SET_IVLEN, 12, NULL) &&
            EVP_EncryptInit_ex(ctx, NULL, NULL, (const unsigned char *)key, (const unsigned char *)nonce))
        {
            if (aadLen > 0 && aad) {
                EVP_EncryptUpdate(ctx, NULL, &outl, (const unsigned char *)aad, (int)aadLen);
            }

            jsize ctTotalLen = ptLen + 16;
            result = (*env)->NewByteArray(env, ctTotalLen);
            if (result) {
                jbyte *outBuf = (*env)->GetPrimitiveArrayCritical(env, result, NULL);
                if (outBuf) {
                    if (EVP_EncryptUpdate(ctx, (unsigned char *)outBuf, &outl, (const unsigned char *)pt, (int)ptLen) &&
                        EVP_EncryptFinal_ex(ctx, (unsigned char *)(outBuf + outl), &finalLen) &&
                        EVP_CIPHER_CTX_ctrl(ctx, EVP_CTRL_AEAD_GET_TAG, 16, (void *)(outBuf + ptLen)))
                    {
                        (*env)->ReleasePrimitiveArrayCritical(env, result, outBuf, 0);
                    } else {
                        (*env)->ReleasePrimitiveArrayCritical(env, result, outBuf, JNI_ABORT);
                        result = NULL;
                    }
                }
            }
        }
    }

    if (ctx) {
        EVP_CIPHER_CTX_free(ctx);
    }

    if (aad && aadArr) (*env)->ReleasePrimitiveArrayCritical(env, aadArr, aad, JNI_ABORT);
    if (pt) (*env)->ReleasePrimitiveArrayCritical(env, ptArr, pt, JNI_ABORT);
    if (nonce) (*env)->ReleasePrimitiveArrayCritical(env, nonceArr, nonce, JNI_ABORT);
    if (key) (*env)->ReleasePrimitiveArrayCritical(env, keyArr, key, JNI_ABORT);

    return result;
}
