import { request, ServiceError } from './api.js';

export type Role = 'coordinator' | 'participant' | 'auditor';
export type Decision = 'agree' | 'rejected' | 'conditional' | 'revoked';
export interface User { id: string; name: string; role: Role; organization_id: string; subjects: string[]; valid_from: string; valid_until: string }
export interface ApprovalContext { source_revision: number; approval_revision: number; active_packet_id: string | null; accepted_packet_id: string | null; accepted_version: number }
export interface ApprovalResponse { id: string; subject_id: string; decision: Decision; expires_at: string; actor_id: string; revision: number; created_at: string; comment?: string }
export interface Subject { id: string; title: string; organization_id: string; sha256: string; details: Record<string, unknown>; revision: number; latest_response: ApprovalResponse | null; can_respond: boolean }
export interface Packet { id: string; version: number; calculation_id: string; status: 'pending' | 'accepted' | 'superseded' | 'stale' | 'review_required'; created_at: string; input_sha256: string; candidate_sha256: string; snapshot_id: string; policy_id: string; source_revision: number; base_accepted_version: number; approval_revision: number; subjects: Subject[]; acceptance: null | { id: string; accepted_at: string; actor_id: string; accepted_version: number }; issues: string[]; can_accept: boolean }
export interface PacketEnvelope { context: ApprovalContext; packet: Packet | null }
export interface PacketList { context: ApprovalContext; packets: Packet[] }

const obj = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x);
const str = (x: unknown): x is string => typeof x === 'string' && x.length > 0;
const num = (x: unknown): x is number => Number.isSafeInteger(x) && Number(x) >= 0;
const stamp = (x: unknown): x is string => str(x) && Number.isFinite(Date.parse(x));
const hash = (x: unknown): x is string => str(x) && /^[a-f0-9]{64}$/i.test(x);
const optionalId = (x: unknown) => x === null || str(x);
const invalid = (): never => { throw new ServiceError('Сервис вернул неподдерживаемые данные согласования. Обновите данные или повторите позже.'); };

function parseUser(value: unknown): User {
  if (!obj(value) || !obj(value.user)) return invalid();
  const u = value.user;
  if (!str(u.id) || !str(u.name) || !['coordinator', 'participant', 'auditor'].includes(String(u.role))
    || !str(u.organization_id) || !Array.isArray(u.subjects) || !u.subjects.every(str) || !stamp(u.valid_from) || !stamp(u.valid_until)) return invalid();
  return { id: u.id, name: u.name, role: u.role as Role, organization_id: u.organization_id, subjects: u.subjects, valid_from: u.valid_from, valid_until: u.valid_until };
}
export function parseContext(x: unknown): ApprovalContext {
  if (!obj(x) || !num(x.source_revision) || !num(x.approval_revision) || !num(x.accepted_version)
    || !optionalId(x.active_packet_id) || !optionalId(x.accepted_packet_id)) return invalid();
  return x as unknown as ApprovalContext;
}
function parseSubject(x: unknown): Subject {
  if (!obj(x) || !str(x.id) || !str(x.title) || !str(x.organization_id) || !hash(x.sha256) || !obj(x.details)
    || !num(x.revision) || typeof x.can_respond !== 'boolean') return invalid();
  const r = x.latest_response;
  if (r !== null && (!obj(r) || !str(r.id) || r.subject_id !== x.id || !['agree', 'rejected', 'conditional', 'revoked'].includes(String(r.decision))
    || !stamp(r.expires_at) || !stamp(r.created_at) || !num(r.revision) || !str(r.actor_id))) return invalid();
  return x as unknown as Subject;
}
function parsePacket(x: unknown): Packet {
  if (!obj(x) || !str(x.id) || !num(x.version) || !str(x.calculation_id)
    || !['pending', 'accepted', 'superseded', 'stale', 'review_required'].includes(String(x.status))
    || !stamp(x.created_at) || !hash(x.input_sha256) || !hash(x.candidate_sha256) || !str(x.snapshot_id) || !str(x.policy_id)
    || !num(x.source_revision) || !num(x.base_accepted_version) || !num(x.approval_revision)
    || !Array.isArray(x.subjects) || !Array.isArray(x.issues) || !x.issues.every(str) || typeof x.can_accept !== 'boolean') return invalid();
  const a = x.acceptance;
  if (a !== null && (!obj(a) || !str(a.id) || !stamp(a.accepted_at) || !str(a.actor_id) || !num(a.accepted_version))) return invalid();
  return { ...x, subjects: x.subjects.map(parseSubject) } as unknown as Packet;
}
function parseEnvelope(x: unknown): PacketEnvelope {
  if (!obj(x)) return invalid();
  return { context: parseContext(x.context), packet: x.packet === null ? null : parsePacket(x.packet) };
}
export const getSession = async (signal?: AbortSignal) => parseUser(await request('/api/v1/session', { signal }));
export const login = async (access_key: string, signal?: AbortSignal) => parseUser(await request('/api/v1/session', {
  method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ access_key }),
}));
export const logout = (signal?: AbortSignal) => request('/api/v1/session', { method: 'DELETE', signal });
export async function getPackets(signal?: AbortSignal): Promise<PacketList> {
  const x = await request('/api/v1/approval-packets', { signal });
  if (!obj(x) || !Array.isArray(x.packets)) return invalid();
  return { context: parseContext(x.context), packets: x.packets.map(parsePacket) };
}
export async function getPacket(id: string, signal?: AbortSignal): Promise<PacketEnvelope> {
  const x = parseEnvelope(await request(`/api/v1/approval-packets/${encodeURIComponent(id)}`, { signal }));
  if (!x.packet || x.packet.id !== id) return invalid();
  return x;
}
export async function mutateApproval(path: string, body: Record<string, unknown>, key: string, signal?: AbortSignal): Promise<PacketEnvelope> {
  return parseEnvelope(await request(path, { method: 'POST', signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key }, body: JSON.stringify(body) }, 15000));
}
