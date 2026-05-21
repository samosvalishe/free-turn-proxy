// Package auth определяет интерфейс Authenticator и типы идентификации тенантов
// для multi-tenant конфигурации.
//
// # Текущее состояние
//
// Реализован только каркас. Везде используется [NopAuthenticator] —
// возвращает [Anonymous], сохраняя single-tenant поведение.
//
// # Планируемый multi-tenant поток
//
//  1. Реальный Authenticator (например HMACAuthenticator поверх SQLite) читает
//     короткий токен из первых байт conn, проверяет его против per-tenant
//     общего секрета и возвращает соответствующий [TenantID].
//  2. bondserver.Registry использует TenantID как часть ключа дедупликации —
//     ConnID разных тенантов не пересекаются.
//  3. tcpfwdserver и udpserver резолвят connectAddr per-tenant — каждый тенант
//     указывает на свой backend.
//  4. Deep-link provisioning доставляет per-tenant секреты мобильным клиентам
//     без ручной настройки.
//
// Ничего из перечисленного пока не реализовано. См. notes/AUTH_PLAN.md.
package auth
