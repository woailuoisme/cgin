# bgin Core Domain

A high-concurrency enterprise web service domain managing identity, authentication, credential lifecycles, and user administration.

## Language

**User**:
A human subject recognized and authorized within the system, holding profile data, status, and role assignments.
_Avoid_: Account (when referring to the human subject), Member, Client

**Account**:
An authentication credential record bound to a User, storing identity provider details, email, and password digest.
_Avoid_: User (when referring to credential records), Login entity

**Session**:
A temporal authentication state tracked via issued JWT access and refresh token pairs, with revoke capabilities.
_Avoid_: Token record, Login state

**Verification**:
A temporary time-bounded single-use token or code for email confirmation or credential reset flows.
_Avoid_: OTP record, Code entity

**Storage Object**:
A binary blob or media asset (such as an avatar or document) persisted through the Storage Disk seam.
_Avoid_: Local file (when referring to abstract storage), S3 item, File record

**Payment Notification**:
An asynchronous, cryptographically verified callback transaction record from external acquisition channels (WeChat Pay, Alipay), idempotently ingested and published as domain events.
_Avoid_: Webhook body, Callback request, Notify model

**Profile Scope**:
An authorization boundary distinguishing personal self-service profile patches (`ScopeClient`) from privileged administrative modifications (`ScopeAdmin`).
_Avoid_: Role tag, Admin flag
