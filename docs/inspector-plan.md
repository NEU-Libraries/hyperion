# Plan: Hyperion as an Atlas object inspector

## Goal

Hyperion becomes a read-only inspector for Atlas objects. A librarian enters a
PID and browses the object from a menu: its properties, relationships, MODS,
METS, files, and permissions. This replaces the object-level view that the
Fedora Commons admin gave librarians in Cerberus V1. MODS export stays as one
feature of the tool.

## Rules

- **Read only.** Hyperion sends GET requests only. The Atlas client has no
  method that can send any other verb.
- **"PID" in the UX.** Anything a user sees says "PID", never "NOID". "NOID" is
  too close to "NUID", the university's person identifier. Go code and Atlas
  field names can keep `noid`.
- **The spec is the contract.** `atlas/openapi/openapi.yaml` matches
  production, so every endpoint Hyperion calls must appear in it.

## Mapping from Fedora to Atlas

| Fedora V1 concept | Atlas endpoint (all GET) |
|---|---|
| Object properties | `/resources/{id}`, which redirects to the typed route |
| Parent relationships | `ancestors` on the typed record, `/files/{id}/ancestry` |
| Child relationships | `…/children`, `/works/{id}/file_sets`, `/compilations/{id}/contents` |
| Other relationships | `/works/{id}/linked_members`, `/works/{id}/associations` |
| descMetadata | `/resources/{id}/mods`, `/resources/{id}/mods/versions` |
| Structural metadata | `/works/{id}/mets`, `/file_sets/{id}/mets` |
| Content datastreams | `/files/{id}`, `/files/{id}/versions`, `/works/{id}/assets` |
| rightsMetadata | `/resources/{id}/permissions` |

Known details:

- `/resources/{id}` answers with a 302 to the typed route. Hyperion reads the
  type from the final URL.
- A Set (Compilation) PID returns 404 on `/resources/{id}`. Hyperion falls back
  to `/compilations/{id}`.
- MODS version history is for admins and delegated admins only. The screen
  says so instead of showing a bare 403.

## Steps

1. **Screen stack.** Split the TUI into screens: login, a top menu, and export.
   Esc goes back one screen. Export behaves as before.
2. **Resolver and overview.** Resolve any PID to its type. Show an overview:
   type, title, ancestor breadcrumb, depositor, tombstone state, permanent URL.
3. **Sections and drill-down.** List the sections that apply to the type.
   Selecting a parent, child, or FileSet opens that object's overview.
4. **Raw view.** Toggle any screen between the formatted view and the raw JSON
   or XML. Save the raw data to a file.
5. **Headless `get`.** Add subcommands: `hyperion export …` and
   `hyperion get <pid> [section]`. The current flags keep working.
