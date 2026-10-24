# kotlinx.serialization: keep generated serializers of the DTOs in :core
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.AnnotationsKt
-keepclassmembers class ir.chador.core.** {
    *** Companion;
    kotlinx.serialization.KSerializer serializer(...);
}
-keep,includedescriptorclasses class ir.chador.core.**$$serializer { *; }
-keepclasseswithmembers class ir.chador.core.** { kotlinx.serialization.KSerializer serializer(...); }
# OkHttp / Okio optional platforms
-dontwarn okhttp3.internal.platform.**
-dontwarn org.conscrypt.**
-dontwarn org.bouncycastle.**
-dontwarn org.openjsse.**
