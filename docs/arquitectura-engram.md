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
- El plugin de Claude Code se instala desde el marketplace del propio fork: `.claude-plugin/marketplace.json` apunta a `plugin/claude-code`, que está en la versión 0.2.0. El plugin busca `engram` en el PATH y no descarga nada.

## Decisiones

| Decisión | Por qué |
|---|---|
| `main` es la única rama y es la línea 1.20 del fork. | El fork no sigue al original. |
| La versión sube a 1.20.x con arreglos y a 1.21.0 con funciones nuevas. | Sigue semver dentro de la línea congelada. |
| El módulo Go sigue siendo `github.com/Gentleman-Programming/engram`. | Renombrarlo toca unos 200 imports y solo sirve para `go install`, que no se usa. |
| El aviso de actualización mira las releases del fork, se actualiza con `pegasus upgrade` + `pegasus update --cli` (o `darq upgrade` + `darq update --cli`), y `ENGRAM_NO_UPDATE_CHECK` lo apaga. | El aviso del original recomendaba saltar a la 3.x con `brew upgrade`. |
| `engram setup claude-code` instala el marketplace del fork. | Antes instalaba el plugin del original. |
| Solo quedan los workflows `ci.yml` y `release.yml`. | Los demás eran políticas o destinos del original y fallan en el fork. |
| Los cambios de esquema de la base solo agregan. | Ver la sección siguiente. |
| `engram purge` se usa solo desde la terminal; no hay herramienta MCP para borrar. | Hoy ningún agente puede borrar en engram. Un agente puede malinterpretar un pedido, o un texto malicioso que lea puede inducirlo a borrar. |
| El plugin de Claude Code no manda avisos por `systemMessage`. | Claude Code los muestra solo a la persona; el modelo nunca los recibe. |

## La base de datos

engram no lleva un número de versión del esquema. `Store.migrate()`, en `internal/store/store.go`, corre en cada apertura y es idempotente. Casi siempre agrega lo que falta: tablas, columnas, índices, triggers de FTS y rellenos de datos. En tres casos heredados del original reconstruye una tabla entera: el índice de búsqueda `observations_fts`, `sync_chunks` y `observations`.

Eso tiene dos consecuencias:

- Un binario nuevo actualiza la base, y ese cambio no tiene vuelta atrás sin un backup.
- Un binario viejo abre una base nueva sin aviso e ignora lo que no conoce.

Varios engram comparten la misma `~/.engram/engram.db`, por ejemplo el de OpenCode y el de Claude Code. Para eso, las reglas del fork son:

1. Las migraciones nuevas del fork solo agregan: tablas, columnas e índices con `IF NOT EXISTS`. Nunca se borra ni se renombra una columna, y no se suman reconstrucciones de tablas a las heredadas.
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

- El paquete npm `gentle-engram@0.1.8`, que `engram setup` instala para otra CLI, y los links de su manifiesto en `plugin/pi/package.json`. Está fijado a una versión, así que no cambia solo.
- El README y los `.md` heredados: unos 50 archivos mencionan el original.
  - Desde la 1.21.0, el README y la documentación ya no prometen Homebrew, la imagen ghcr ni el marketplace del original. Lo que queda son menciones históricas o créditos.
  - `docs/engram-cloud/`, incluido su `docker-compose.ghcr.yml`, sigue nombrando la imagen Docker del original, que el fork no publica, pero ahora lo avisa en un banner.
- El módulo Go y sus imports, a propósito (ver Decisiones).
- El autor de los plugins sigue siendo el del original, como crédito de su código. El dueño del marketplace es el fork. Cuando el fork modifique un plugin, Sergio Balerdi se suma a sus autores.
- Un comentario en `internal/server/server.go` que cita un issue del original y las URLs de ejemplo de `internal/project/detect_test.go`. Son inertes.

El fork tiene los issues y las discusiones deshabilitados, así que las plantillas de `.github/ISSUE_TEMPLATE/` no se usan. Dependabot tampoco corre en un fork hasta que se lo habilita a mano.

## Ideas

Las ideas se acumulan acá y se toman por release.

- **Tapar credenciales en los prompts que guarda el plugin de Claude Code.** Hoy se guardan tal como se escriben: el servidor solo saca `<private>`. Habría que llevar a engram el catálogo de credenciales que usa Pegasus.
- **Prompts muy largos.** El plugin arma el envío con argumentos de shell, así que un prompt de más de unos 128 KB no se guarda, sin aviso.
- **Una herramienta para agentes que solo muestre qué borraría `purge`**, sin borrar nunca, por si hace falta que un agente ayude a encontrar lo que sobra.
- **El SessionStart del plugin migra el nombre del proyecto.** En cada arranque, si el nombre de la carpeta difiere del remoto de git, llama a `/projects/migrate`, y eso podría fusionar proyectos de laboratorio. Se dejó como está a propósito en la 1.21.0, por decisión del usuario.
- **Decidir qué hacer con lo del paquete npm `gentle-engram`** (ver la sección anterior).

## Releases

### 1.21.0

1. **`engram purge`, para borrar lo que dejan las corridas de prueba.** Se elige por proyecto, por sesión o por rango de fechas, y los criterios se combinan con «y».
   - Sin `--yes` solo muestra qué borraría.
   - Con `--yes` hace un backup `engram-purge-<fecha>.db` (archivo `0600`; la carpeta, `0700` si la crea), lo verifica (`integrity_check` y conteos) y recién entonces borra todo en una sola transacción. Si el backup falla o no verifica, no borra nada.
   - Nunca toca `sync_chunks`, así lo borrado no vuelve al reimportar chunks ya conocidos. Avisa si algo pudo haberse exportado a archivos `.engram/` de algún repo.
   - Se niega con memorias fijadas (salvo `--include-pinned`) y con proyectos sincronizados con la nube, sin excepción.
   - Las filas con una fecha que no se puede leer nunca entran por un filtro de fechas, y el plan dice cuántas quedaron afuera. También dice cuántas relaciones borradas apuntan a memorias que se conservan.
   - No consulta si hay versiones nuevas.
   - Probado sobre una copia de una base real: borró 3668 sesiones vacías de un proyecto de sondeos, más sus 6268 mutaciones pendientes, en 1,5 s, sin tocar memorias ni prompts.
2. **`engram delete project`** encuentra todas las escrituras de un nombre que se normalizan igual (por ejemplo, con mayúsculas).
3. **Terminar una sesión sin resumen ya no borra el resumen guardado** (`EndSession` con `COALESCE`).
4. **El aviso de actualización** dice los dos pasos: `pegasus upgrade` y después `pegasus update --cli <cli>`, y lo mismo con `darq`.
5. **El plugin de Claude Code pasa a 0.2.0**, con Sergio Balerdi entre sus autores:
   - `SubagentStop` lee `last_assistant_message`. Antes leía `stdout`, que Claude Code no manda, y nunca capturó nada.
   - La sesión se termina en `SessionEnd`, no en `Stop`, que corre después de cada respuesta.
   - `SessionStart` registra también las sesiones retomadas y bifurcadas (`startup|resume|clear|fork`).
   - Se quitaron el aviso de cargar herramientas del primer mensaje y el recordatorio de guardar cada 15 minutos: iban por `systemMessage`, que el modelo no recibe, y lo primero ya lo cubre `SessionStart`.
   - `UserPromptSubmit` queda, en segundo plano, solo para guardar cada prompt en `/prompts`, como antes. Una primera versión lo había quitado creyendo que solo mandaba los avisos; la revisión lo detectó.
   - El texto del protocolo nombra bien las herramientas del plugin y ya no anuncia las que el perfil de agentes no registra.
   - `engram serve` se lanza con `setsid`, y los archivos de estado usan `TMPDIR`.
6. **El README y la documentación heredada** dicen que es un fork y cómo instalarlo, y ya no ofrecen Homebrew, la imagen del original ni su marketplace.

No cambia el esquema de la base: la 1.21.0 y la 1.20.x pueden abrir la misma base.

### 1.20.1

Es una puesta a punto, sin funciones nuevas:

- El aviso de actualización mira las releases del fork, con el texto «pegasus update (or: darq update)», que estaba incompleto y se corrigió en la 1.21.0, y `ENGRAM_NO_UPDATE_CHECK` lo apaga.
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
