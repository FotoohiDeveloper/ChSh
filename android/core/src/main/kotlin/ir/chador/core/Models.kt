package ir.chador.core

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement

/** One JSON configuration for the whole app: tolerant of fields added by newer servers. */
val ChadorJson: Json = Json {
    ignoreUnknownKeys = true
    explicitNulls = false     // null request fields are omitted (the server rejects unknown fields, not missing ones)
    encodeDefaults = false
    coerceInputValues = true
}

// ---- auth / users ----

@Serializable
data class TokenPair(
    @SerialName("access_token") val accessToken: String,
    @SerialName("refresh_token") val refreshToken: String,
    @SerialName("expires_at") val expiresAt: String = "",
)

@Serializable
data class ShiftView(
    val id: String,
    @SerialName("place_id") val placeId: Int,
    @SerialName("place_name") val placeName: String = "",
    @SerialName("door_id") val doorId: Int? = null,
    @SerialName("door_name") val doorName: String? = null,
    @SerialName("started_at") val startedAt: String = "",
)

@Serializable
data class UserView(
    val id: String,
    val username: String,
    @SerialName("full_name") val fullName: String,
    val role: String,
    val active: Boolean = true,
    @SerialName("must_change_password") val mustChangePassword: Boolean = false,
    val shift: ShiftView? = null,
) {
    val roleEnum: Role get() = Role.from(role)
}

@Serializable
data class LoginResponse(val tokens: TokenPair, val user: UserView)

@Serializable
data class RefreshResponse(val tokens: TokenPair)

@Serializable
data class CreatedUser(
    val id: String,
    val username: String,
    @SerialName("full_name") val fullName: String,
    val role: String,
    val active: Boolean = true,
    @SerialName("must_change_password") val mustChangePassword: Boolean = true,
    @SerialName("temp_password") val tempPassword: String,
)

@Serializable
data class UsersResponse(val users: List<UserView> = emptyList())

@Serializable
data class TempPasswordResponse(@SerialName("temp_password") val tempPassword: String)

@Serializable
data class Colleague(val id: String, @SerialName("full_name") val fullName: String, val role: String)

@Serializable
data class ColleaguesResponse(val colleagues: List<Colleague> = emptyList())

// ---- places ----

@Serializable
data class Door(val id: Int, val name: String)

@Serializable
data class Place(val id: Int, val kind: String, val name: String, val doors: List<Door> = emptyList())

// ---- pilgrims ----

@Serializable
data class PilgrimView(
    val id: String,
    val verification: String,
    @SerialName("full_name") val fullName: String? = null,
    @SerialName("national_id_masked") val nationalIdMasked: String = "",
    @SerialName("mobile_masked") val mobileMasked: String = "",
    @SerialName("active_tents") val activeTents: List<String> = emptyList(),
    val existing: Boolean = false,
    /** Server says tents may be handed out (verified, or unverified while the pilot mode allows it). */
    val usable: Boolean? = null,
) {
    val verified: Boolean get() = usable ?: (verification == "verified")
}

@Serializable
data class PilgrimInput(
    @SerialName("national_id") val nationalId: String,
    @SerialName("birth_date") val birthDate: String,
    val mobile: String,
)

// ---- tents ----

@Serializable
data class ActiveAssignment(
    val id: String,
    @SerialName("pilgrim_id") val pilgrimId: String,
    @SerialName("pilgrim_name") val pilgrimName: String? = null,
    @SerialName("mobile_masked") val mobileMasked: String? = null,
    @SerialName("assigned_at") val assignedAt: String = "",
    @SerialName("assigned_place") val assignedPlace: String? = null,
)

@Serializable
data class TentView(
    val code: String,
    val status: String,
    @SerialName("place_id") val placeId: Int? = null,
    @SerialName("place_name") val placeName: String? = null,
    val kind: String = "standard",
    val note: String? = null,
    val assignment: ActiveAssignment? = null,
) {
    val statusEnum: TentStatus get() = TentStatus.from(status)
}

@Serializable
data class TentEvent(
    val at: String,
    val type: String,
    val from: String? = null,
    val to: String? = null,
    @SerialName("place_id") val placeId: Int? = null,
    @SerialName("door_id") val doorId: Int? = null,
    val user: String = "",
    val meta: JsonElement? = null,
)

@Serializable
data class HistoryResponse(val events: List<TentEvent> = emptyList())

// ---- operations (requests carry op_id/client_ts so retries and offline replays are safe) ----

@Serializable
data class AssignRequest(
    @SerialName("pilgrim_id") val pilgrimId: String,
    val codes: List<String>,
    @SerialName("socks_qty") val socksQty: Int = 0,
    @SerialName("op_id") val opId: String? = null,
    @SerialName("client_ts") val clientTs: String? = null,
)

@Serializable
data class AssignItem(@SerialName("assignment_id") val assignmentId: String, val code: String)

@Serializable
data class AssignResponse(
    val items: List<AssignItem> = emptyList(),
    @SerialName("socks_qty") val socksQty: Int = 0,
    @SerialName("socks_amount") val socksAmount: Long = 0,
)

@Serializable
data class ReturnItemRequest(val code: String, val status: String = "ready")

@Serializable
data class ReturnRequest(
    val items: List<ReturnItemRequest>,
    @SerialName("op_id") val opId: String? = null,
    @SerialName("client_ts") val clientTs: String? = null,
)

@Serializable
data class ReturnResult(val code: String, val ok: Boolean = false, val error: String? = null, val detail: String? = null)

@Serializable
data class ReturnResponse(
    val results: List<ReturnResult> = emptyList(),
    val returned: Int = 0,
    @SerialName("completed_pilgrims") val completedPilgrims: Int = 0,
)

@Serializable
data class CancelRequest(val code: String, @SerialName("op_id") val opId: String? = null)

@Serializable
data class CancelResponse(val code: String)

@Serializable
data class StatusRequest(
    val code: String,
    val to: String,
    val note: String? = null,
    @SerialName("op_id") val opId: String? = null,
    @SerialName("client_ts") val clientTs: String? = null,
)

@Serializable
data class StatusResponse(val code: String, val from: String, val to: String)

@Serializable
data class MoveRequest(
    val codes: List<String>,
    @SerialName("to_place_id") val toPlaceId: Int,
    @SerialName("op_id") val opId: String? = null,
    @SerialName("client_ts") val clientTs: String? = null,
)

@Serializable
data class MoveSkipped(val code: String, val reason: String)

@Serializable
data class MoveResponse(val moved: List<String> = emptyList(), val skipped: List<MoveSkipped> = emptyList())

@Serializable
data class SocksRequest(
    val kind: String,
    val qty: Int,
    @SerialName("place_id") val placeId: Int? = null,
    @SerialName("pilgrim_id") val pilgrimId: String? = null,
    @SerialName("op_id") val opId: String? = null,
    @SerialName("client_ts") val clientTs: String? = null,
)

@Serializable
data class SocksResponse(val kind: String, val qty: Int, val amount: Long = 0, val balance: Int = 0)

// ---- retire, handover ----

@Serializable
data class RetireRequest(val code: String, val reason: String, val note: String? = null)

@Serializable
data class IdResponse(val id: String)

@Serializable
data class RetireRequestView(
    val id: String,
    val code: String,
    val reason: String,
    val note: String? = null,
    val state: String = "pending",
    @SerialName("requested_by") val requestedBy: String = "",
    @SerialName("requested_at") val requestedAt: String = "",
)

@Serializable
data class RetireListResponse(val requests: List<RetireRequestView> = emptyList())

@Serializable
data class DecisionRequest(val approve: Boolean)

@Serializable
data class HandoverRequest(@SerialName("to_user") val toUser: String, val notes: String? = null)

@Serializable
data class HandoverView(
    val id: String,
    val place: String = "",
    val from: String = "",
    val to: String = "",
    val snapshot: JsonElement? = null,
    val notes: String? = null,
    val state: String = "pending",
    @SerialName("created_at") val createdAt: String = "",
)

@Serializable
data class HandoverListResponse(val handovers: List<HandoverView> = emptyList())

@Serializable
data class ResolveRequest(val accept: Boolean)

// ---- dashboard / reports ----

@Serializable
data class PlaceStat(
    @SerialName("place_id") val placeId: Int,
    val name: String,
    val kind: String,
    @SerialName("by_status") val byStatus: Map<String, Int> = emptyMap(),
    @SerialName("socks_balance") val socksBalance: Int = 0,
    @SerialName("tents_out_from_here") val outFromHere: Int = 0,
)

@Serializable
data class Today(val assigned: Int = 0, val returned: Int = 0)

@Serializable
data class Dashboard(
    @SerialName("generated_at") val generatedAt: String = "",
    val scope: String = "all",
    val totals: Map<String, Int> = emptyMap(),
    val places: List<PlaceStat> = emptyList(),
    val today: Today = Today(),
    val overdue: Int = 0,
)

@Serializable
data class PlaceDay(
    val name: String,
    val assigned: Int = 0,
    val returned: Int = 0,
    @SerialName("socks_qty") val socksQty: Int = 0,
    @SerialName("socks_amount") val socksAmount: Long = 0,
)

@Serializable
data class OverdueRow(
    val code: String,
    @SerialName("assigned_at") val assignedAt: String = "",
    val place: String = "",
    val reminders: Int = 0,
)

@Serializable
data class DailyReport(
    val date: String,
    val assigned: Int = 0,
    val returned: Int = 0,
    val cancelled: Int = 0,
    val lost: Int = 0,
    @SerialName("socks_qty") val socksQty: Int = 0,
    @SerialName("socks_amount") val socksAmount: Long = 0,
    val places: List<PlaceDay> = emptyList(),
    @SerialName("status_now") val statusNow: Map<String, Int> = emptyMap(),
    val overdue: List<OverdueRow> = emptyList(),
)

// ---- admin ----

@Serializable
data class CreateUserRequest(val username: String, @SerialName("full_name") val fullName: String, val role: String)

@Serializable
data class UpdateUserRequest(
    @SerialName("full_name") val fullName: String? = null,
    val role: String? = null,
    val active: Boolean? = null,
)

@Serializable
data class AuditRow(
    val id: Long,
    val at: String,
    val user: String = "",
    val action: String,
    val entity: String = "",
    @SerialName("entity_id") val entityId: String = "",
    val ip: String = "",
)

@Serializable
data class AuditResponse(val records: List<AuditRow> = emptyList())

// ---- sync envelope ----

@Serializable
data class SyncOpDto(
    @SerialName("op_id") val opId: String,
    val type: String,
    @SerialName("shift_id") val shiftId: String? = null,
    @SerialName("client_ts") val clientTs: String? = null,
    val payload: JsonElement,
)

@Serializable
data class SyncRequest(val ops: List<SyncOpDto>)

@Serializable
data class ErrorBody(val code: String = "", val message: String = "", val details: JsonElement? = null)

@Serializable
data class SyncResultDto(
    @SerialName("op_id") val opId: String,
    val status: String,
    val error: ErrorBody? = null,
)

@Serializable
data class SyncResponse(val results: List<SyncResultDto> = emptyList())

@Serializable
data class ErrorEnvelope(val error: ErrorBody? = null, @SerialName("request_id") val requestId: String? = null)
