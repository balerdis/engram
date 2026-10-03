# Arquitectura de engram (fork balerdis/engram)

Este documento reúne las decisiones, las reglas y las ideas del fork. Lo que es de Pegasus Harness o de DARQ queda en su propia documentación. Ahí solo figura qué versión de engram descargan y desde dónde.

## Por qué existe el fork

- engram queda congelado en la 1.20.0. Las versiones siguientes del original van en una dirección que no queremos seguir.
- El original no publica arreglos para la 1.20: la v1.20.0 salió el 2026-07-20, nunca hubo una 1.20.x, y su política de seguridad solo cubre la última versión estable.
- El fork arranca en la v1.20.0 (commit `ba9e46c`) y evoluciona por su cuenta. No se mezcla nada del original.
- Para mirar qué hace el original sin traerlo, se usa `gh api`. `gh api repos/balerdis/engram --jq .parent.full_name` da el nombre del repo.

## Cómo se distribuye

- engram es una dependencia opcional de Pegasus Harness y de DARQ, y no se distribuye por separado. No hay Homebrew, ni paquete npm, ni imagen Docker.
- Al empujar una etiqueta `v*`, el workflow `release.yml` corre goreleaser. goreleaser publica en la release de GitHub los binarios para linux, darwin y windows, en amd64 y arm64, junto con `checksums.txt`.
- Pegasus y DARQ fijan la versión, la URL del tar y su checksum en `content/mcp/engram.md`. Por eso cada release del fork va acompañada de una release de Pegasus y de DARQ.
- El plugin de Claude Code se instala desde el marketplace del propio fork: `.claude-plugin/marketplace.json` apunta a `plugin/claude-code`, que está en la versión 0.1.1. El plugin busca `engram` en el PATH y no descarga nada.

## Decisiones

| Decisión | Por qué |
|---|---|
| `main` es la única rama y es la línea 1.20 del fork. | El fork no sigue al original. |
| La versión sube a 1.20.x con arreglos y a 1.21.0 con funciones nuevas. | Sigue semver dentro de la línea congelada. |
| El módulo Go sigue siendo `github.com/Gentleman-Programming/engram`. | Renombrarlo toca unos 200 imports y solo sirve para `go install`, que no se usa. |
| El aviso de actualización mira las releases del fork, se actualiza con `pegasus update` o `darq update`, y `ENGRAM_NO_UPDATE_CHECK` lo apaga. | El aviso del original recomendaba saltar a la 3.x con `brew upgrade`. |
| `engram setup claude-code` instala el marketplace del fork. | Antes instalaba el plugin del original. |
| Solo quedan los workflows `ci.yml` y `release.yml`. | Los demás eran políticas o destinos del original y fallan en el fork. |
| Los cambios de esquema de la base solo agregan. | Ver la sección siguiente. |

## La base de datos

engram no lleva un número de versión del esquema. `Store.migrate()`, en `internal/store/store.go`, corre en cada apertura. Es idempotente y agrega lo que falta: tablas, columnas, índices, triggers de FTS y rellenos de datos.

Eso tiene dos consecuencias:

- Un binario nuevo actualiza la base, y ese cambio no tiene vuelta atrás sin un backup.
- Un binario viejo abre una base nueva sin aviso e ignora lo que no conoce.

Varios engram comparten la misma `~/.engram/engram.db`, por ejemplo el de OpenCode y el de Claude Code. Para eso, las reglas del fork son:

1. Las migraciones solo agregan: tablas, columnas e índices con `IF NOT EXISTS`. Nunca se borra ni se renombra una columna, y nunca se reconstruye una tabla.
2. Antes de estrenar una versión que toca la base, se hace un backup con la API de backup de sqlite, por ejemplo `sqlite3 engram.db ".backup engram-<fecha>.db"`. Copiar el archivo mientras engram corre no sirve, porque la base usa WAL.
3. Todos los engram que abren la misma base pasan juntos a la versión nueva.

## Conexiones de red del binario

| Qué | Cuándo |
|---|---|
| El aviso de actualización (API de releases de GitHub, con 2 s de timeout) | En los comandos de CLI y al abrir la TUI. Nunca en `mcp`, `serve`, `protocol-mode` ni `cloud serve`, que son los modos que usan OpenCode y Claude Code. |
| La sincronización con la nube | Solo con `sync --cloud` o los comandos `cloud`, o con `ENGRAM_CLOUD_AUTOSYNC=1` más un servidor y un token configurados. |
| Telemetría | No hay. |

## Lo que todavía apunta al original

Desde la 1.20.1, ningún flujo del binario ni del instalador lleva al original. Lo que queda:

- El paquete npm `gentle-engram@0.1.8`, que `engram setup` instala para otra CLI, y su manifiesto en `plugin/pi/package.json`. Está fijado a una versión, así que no cambia solo.
- El README y la documentación en `docs/`, que tienen 213 menciones en 28 archivos.
- El módulo Go y sus imports, a propósito (ver Decisiones).
- Un comentario en `internal/server/server.go` que cita un issue del original y las URLs de ejemplo de `internal/project/detect_test.go`. Son inertes.

El fork tiene los issues y las discusiones deshabilitados, así que las plantillas de `.github/ISSUE_TEMPLATE/` no se usan. Dependabot tampoco corre en un fork hasta que se lo habilita a mano.

## Ideas

Las ideas se acumulan acá y se toman por release.

- **Borrar las memorias de las corridas de prueba.** Las pruebas en vivo de Pegasus y DARQ dejan observaciones en la base real. Se podría borrar por proyecto, por sesión o por rango de fechas, mostrando antes lo que se va a borrar.
- **Un README propio:** que diga que es un fork y que se instala con Pegasus o DARQ.
- **Decidir qué hacer con lo del paquete npm `gentle-engram`** (ver la sección anterior).

## Releases

### 1.20.1 (en preparación)

Es una puesta a punto, sin funciones nuevas:

- El aviso de actualización mira las releases del fork, con el texto de `pegasus update` / `darq update`, y `ENGRAM_NO_UPDATE_CHECK` lo apaga.
- `engram setup claude-code` instala el plugin desde el marketplace del fork.
- La publicación ya no actualiza Homebrew.
- Se quitan los workflows `pr-check`, `stale`, `cloud-image` y `publish-pi`.
- Los metadatos del repo y de los plugins apuntan al fork: `CODEOWNERS`, dependabot, las etiquetas, las plantillas de issues y los manifiestos de los plugins.

Para publicarla:

1. Empujar la etiqueta `v1.20.1`.
2. Verificar los assets contra `checksums.txt`.
3. Subir la versión de engram en Pegasus y DARQ (`content/mcp/engram.md`: endpoint, `version` y checksum del tar linux_amd64) y publicar sus releases.
4. Reemplazar a mano los binarios de engram que no instaló Pegasus.

### 1.20.0

Igual al original byte a byte: la release tiene los 7 assets de la v1.20.0 original, verificados uno por uno. Desde Pegasus y DARQ 7.6.1, los dos la descargan desde este fork.
