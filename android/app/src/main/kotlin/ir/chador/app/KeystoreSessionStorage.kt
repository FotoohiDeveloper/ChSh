package ir.chador.app

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import ir.chador.core.ChadorJson
import ir.chador.core.SessionStorage
import ir.chador.core.StoredSession
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Keeps the login tokens on disk encrypted with an AES-256-GCM key that lives in the Android Keystore
 * (the key never leaves secure hardware where the device has it). If the file or key is unusable
 * (restored backup, wiped key, tampering) the session is simply dropped and the user logs in again.
 */
class KeystoreSessionStorage(private val file: File) : SessionStorage {
    private val alias = "chador_session_v1"

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(alias, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    @Synchronized
    override fun load(): StoredSession? = try {
        if (!file.exists()) null else {
            val raw = file.readBytes()
            val iv = raw.copyOfRange(0, IV_LEN)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, iv)) }
            val plain = cipher.doFinal(raw, IV_LEN, raw.size - IV_LEN)
            ChadorJson.decodeFromString(StoredSession.serializer(), String(plain, Charsets.UTF_8))
        }
    } catch (e: Exception) {
        clear()
        null
    }

    @Synchronized
    override fun save(s: StoredSession) {
        try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key()) }
            val ct = cipher.doFinal(ChadorJson.encodeToString(StoredSession.serializer(), s).toByteArray(Charsets.UTF_8))
            val tmp = File(file.parentFile, file.name + ".tmp")
            tmp.writeBytes(cipher.iv + ct)
            if (!tmp.renameTo(file)) { file.delete(); tmp.renameTo(file) }
        } catch (e: Exception) {
            // cannot store securely: better to ask for a login next time than to store in clear text
            clear()
        }
    }

    @Synchronized
    override fun clear() {
        file.delete()
    }

    private companion object { const val IV_LEN = 12 }
}
