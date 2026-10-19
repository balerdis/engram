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
- El plugin de Claude Code se instala desde el marketplace del propio fork: `.claude-plugin/marketplace.json` apunta a `plugin/claude-code`, que está en la versión 0.3.0. El plugin busca `engram` en el PATH y no descarga nada.

## Decisiones

| Decisión | Por qué |
|---|---|
| `main` es la única rama y es la línea 1.20 del fork. | El fork no sigue al original. |
| La versión sube a 1.20.x con arreglos y a 1.21.0 y 1.22.0 con funciones nuevas. | Sigue semver dentro de la línea congelada. |
| El módulo Go sigue siendo `github.com/Gentleman-Programming/engram`. | Renombrarlo toca unos 200 imports y solo sirve para `go install`, que no se usa. |
| El aviso de actualización mira las releases del fork, se actualiza con `pegasus upgrade` + `pegasus update --cli` (o `darq upgrade` + `darq update --cli`), y `ENGRAM_NO_UPDATE_CHECK` lo apaga. | El aviso del original recomendaba saltar a la 3.x con `brew upgrade`. |
| `engram setup claude-code` instala el marketplace del fork. | Antes instalaba el plugin del original. |
| Solo quedan los workflows `ci.yml` y `release.yml`. | Los demás eran políticas o destinos del original y fallan en el fork. |
| Los cambios de esquema de la base solo agregan. | Ver la sección siguiente. |
| `engram purge` se usa solo desde la terminal; no hay herramienta MCP para borrar. | Hoy ningún agente puede borrar en engram. Un agente puede malinterpretar un pedido, o un texto malicioso que lea puede inducirlo a borrar. |
| El plugin de Claude Code no manda avisos por `systemMessage`. | Claude Code los muestra solo a la persona; el modelo nunca los recibe. |
| La sincronización con la nube (engram-cloud) se quitó entera en la 1.22.0. | Nadie la usa, arrastraba unas 60 000 líneas y dependía de una imagen y un servidor del original. Queda `engram sync` local. |
| El puerto de engram es propio de cada usuario del sistema, con la regla que ya usa Pegasus. | Con un puerto fijo, el engram de un usuario atendía a los clientes de otro y mezclaba sus memorias. |
| El servidor tapa las credenciales en todo texto libre que guarda, con el catálogo de Pegasus vendorizado. | Antes cada plugin decidía si taparlas; los prompts se guardaban tal cual. |
| Solo guarda memorias el agente que habla con la persona; es una guía en el protocolo y un perfil `reader` opcional, no un bloqueo. | El servidor no distingue al orquestador de un subagente. |

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
| El aviso de actualización (API de releases de GitHub, con 2 s de timeout) | En los comandos de CLI y al abrir la TUI. Nunca en `mcp`, `serve`, `protocol-mode`, `purge`, `port` ni `redact`, que son los modos que usan OpenCode y Claude Code o que se corren en scripts. |
| La sincronización con la nube | Ninguna. Desde la 1.22.0 no hay cliente ni servidor de nube: `engram sync` solo exporta e importa chunks locales. |
| Telemetría | No hay. |

## Lo que todavía apunta al original

Desde la 1.20.1, ningún flujo del binario ni del instalador lleva al original. Lo que queda:

- Los demás destinos de `engram setup` (por ejemplo `gemini-cli` o `codex`) todavía copian archivos o configuraciones heredadas del original. Se revisan uno por uno cuando alguien los use.
- El README y los `.md` heredados: unos 50 archivos mencionan el original.
  - Desde la 1.21.0, el README y la documentación ya no prometen Homebrew, la imagen ghcr ni el marketplace del original. Lo que queda son menciones históricas o créditos.
  - Desde la 1.22.0 no existe `docs/engram-cloud/` ni el resto de lo de la nube, así que ya no queda ninguna referencia a la imagen Docker del original.
- El módulo Go y sus imports, a propósito (ver Decisiones).
- El autor de los plugins sigue siendo el del original, como crédito de su código. El dueño del marketplace es el fork. Cuando el fork modifique un plugin, Sergio Balerdi se suma a sus autores.
- Un comentario en `internal/server/server.go` que cita un issue del original y las URLs de ejemplo de `internal/project/detect_test.go`. Son inertes.

El fork tiene los issues y las discusiones deshabilitados, así que las plantillas de `.github/ISSUE_TEMPLATE/` no se usan. Dependabot tampoco corre en un fork hasta que se lo habilita a mano.

## Ideas

Las ideas se acumulan acá y se toman por release.

- **Prompts muy largos.** El plugin arma el envío con argumentos de shell, así que un prompt de más de unos 128 KB no se guarda, sin aviso.
- **Una herramienta para agentes que solo muestre qué borraría `purge`**, sin borrar nunca, por si hace falta que un agente ayude a encontrar lo que sobra.
- **Restos de la nube.** Quedan la tabla `sync_apply_deferred` (solo lectura, y un `DELETE FROM sync_apply_deferred` en `applyRelationUpsertTx` que ya no tiene qué borrar), `CountDeferredAndDead` en las estadísticas de conflictos y los errores `ErrRelationFKMissing` y `ErrApplyDead`. Se pueden quitar sin tocar el esquema.
- **`engram redact --scan` para las filas viejas.** El servidor no reescribe lo que ya está guardado. Un comando que solo busque credenciales en la base, y diga dónde, dejaría decidir qué hacer con ellas.
- **Rendimiento del redactor.** Cuesta unos 45 ms por observación de 50 KB (cerca de 1 MB/s) y unos 250 ms en el peor caso, 262 144 bytes. Se puede optimizar `internal/redact` si molesta.
- **Otros destinos de `engram setup` que todavía apuntan al original** (ver la sección anterior).
- **Huecos del catálogo de credenciales**, decididos para la próxima versión (Pegasus 7.9.1 y engram 1.22.1, porque el catálogo es de Pegasus y se vendoriza): claves en camelCase (`dbPassword`), `*_SECRET_KEY` y `AWS_SECRET_ACCESS_KEY`, contraseñas sin dígitos, `sk-ant-` y `sk-proj-` con guiones, `glpat-`, `AIza`, `npm_`, bloques PEM sin `END` y bloques de clave privada PGP, comillas escapadas que dejan filtrar el resto del valor, y `<private>` anidado o sin cerrar.
- **`type` y `directory` no se sanean** (tampoco `project`, `scope` ni los IDs).
- **Costo de leer `/proc` en cada conexión.** La guarda de uid lee `/proc/net/tcp` (y `tcp6`) al aceptar cada conexión nueva; una conexión keep-alive lo paga una sola vez. Si molesta, se puede cachear.
- **`StartSession` no toca `started_at`.** Una sesión vieja que se retoma pierde frente a una sesión abierta más nueva en `MostRecentActiveSession`.
- **Avisar de un `serve` viejo.** Tras actualizar puede seguir vivo el `serve` anterior. Los plugins podrían avisar cuando `/health` informe una versión menor a la que esperan.
- **Prueba inestable de `internal/obsidian`** (`TestWatcherRunsImmediatelyThenTicks`, con una ventana de menos de 5 ms): falla a veces con la máquina cargada. Ya estaba antes de la 1.22.0.- **Filas heredadas con un nombre de proyecto no canónico.** Hay que fusionarlas a mano con `mem_merge_projects` (`MergeProjects`). `/projects/migrate` ya no las mueve si el destino tiene filas.

## Releases

### 1.22.0

1. **engram-cloud se quita entero.** Nadie lo usaba y arrastraba unas 60 000 líneas.
   - Se fueron el cliente y el servidor de nube, sus comandos y `sync --cloud`, el autosync y la documentación y la imagen Docker de `docs/engram-cloud/`.
   - Se queda `engram sync` local, con sus chunks y la tabla `sync_chunks`, y las filas `local` y `cloud` de `sync_state` (`migrate()` recrea la `cloud` en cada apertura).
   - Nada escribe en `sync_mutations`. Las cinco tablas de la nube siguen en el esquema, sin ningún `DROP`, para no romper a una 1.21.x que abra la misma base.
   - `chunkcodec` pasó a `internal/sync/chunkcodec`, con su formato fijado por un test.
   - `engram purge` ya no se niega por proyectos enrolados, `engram doctor` pierde el chequeo `sync_mutation_required_fields` (quedan tres) y `ProjectExists` ya no cuenta los enrolados.
2. **`engram purge --sync-leftovers`**, para limpiar lo que dejó la sincronización eliminada (en la base del usuario eran unas 46 612 filas y 47 MB).
   - Sin `--yes` es un ensayo: dice qué borraría.
   - Con `--yes` hace un backup `0600` y lo verifica antes de borrar. Conserva `sync_chunks` y las filas `local` y `cloud` de `sync_state`.
   - `--vacuum` reescribe el archivo para devolver el espacio. Pide que `serve` y `mcp` estén detenidos: lo comprueba con `wal_checkpoint(TRUNCATE)` antes de borrar y, si no logra exclusividad, rechaza a los cinco segundos (`busy_timeout`).
   - Acepta `--json`.
3. **Un puerto propio por usuario del sistema.**
   - `engram port` imprime el puerto de este usuario (solo el número). Si la memoria queda deshabilitada, sale con código 1 y explica por qué en stderr. `engram serve` usa la misma resolución.
   - La regla es la que Pegasus ya tiene del lado cliente, con sus vectores de prueba copiados en `internal/portowner/testdata/` (con un `ORIGEN.txt`; si la regla cambia allá, hay que recopiarlos): `ENGRAM_PORT` válido si nadie o este usuario escucha ahí; si no, 7437 con la misma condición; si no, `17437 + uid % 10000`. El puerto derivado se calcula, no se reserva. El dueño se lee de `/proc/net/tcp` y `tcp6`.
   - El servidor rechaza con 403 (`foreign_uid`) a un cliente de otro uid, salvo en `/health`. `root` pasa. `ENGRAM_ALLOW_OTHER_USERS=1` apaga la guarda.
   - El dueño se resuelve al aceptar la conexión, cuando el socket del cliente todavía está establecido, y se guarda para esa conexión. Solo cuentan las filas `ESTABLISHED` (`01`); `root` solo cuenta si la fila está establecida. Un cliente que manda la petición y cierra enseguida dejaba una fila `TIME_WAIT` con uid 0 (o ninguna) y pasaba como `root` o por falla abierta.
   - Falla cerrado: con `/proc` legible, un cliente de loopback sin fila establecida recibe 403, y un error de lectura a mitad de la tabla cuenta como «sin fila». Falla abierto solo si `/proc/net/tcp` no existe o no se puede abrir, con `ENGRAM_ALLOW_OTHER_USERS=1`, o si el cliente no es de loopback.
   - `/health` informa el `uid` y la versión real (`dev` si no se compiló con una).
4. **El servidor tapa las credenciales en todo texto libre que guarda.**
   - Orden: cortar a 262 144 bytes, `Redact`, sacar `<private>` y truncar por runas (con `... [truncated]` si se cortó).
   - Cubre título, contenido, `topic_key`, `tool_name`, prompts, el resumen de `EndSession` y el `reason` y `evidence` de las relaciones, y también la importación y los chunks que se aplican. No cubre `project`, `directory`, `type`, `scope` ni los IDs.
   - Cada credencial queda como `[REDACTED:NOMBRE]`. Un `<private>texto</private>` sin nombre ahora queda `[REDACTED:PRIVATE]` (antes `[REDACTED]`).
   - La deduplicación se hace sobre el texto ya tapado. Las filas que ya estaban guardadas no se reescriben.
   - `engram redact` filtra stdin a stdout, y `engram redact --catalog-sha256` imprime el hash del catálogo.
   - El catálogo (`internal/redact/catalog.json`) es el de Pegasus, vendorizado byte a byte, con su hash fijado en `catalog_test.go`. `make sync-catalog CATALOG=<ruta>` lo actualiza. Al actualizarlo hay que cambiar el hash, regenerar `testdata/cases.json` y revisar los chequeos de carga.
   - Los 237 casos de oro se generaron con el redactor de Pegasus. Hay un desvío a propósito: Go reemplaza por posición, mientras que el redactor de Pegasus reemplazaba la primera aparición del valor y corrompía una URL cuyo usuario era igual a la contraseña. Pegasus lo arregló en su deuda 28 y su salida da el mismo `cases.json`. Los valores de más de 1024 caracteres nunca cuentan como placeholder y se tapan.
   - Cuesta unos 45 ms por observación de 50 KB.
5. **Solo guarda memorias el agente que habla con la persona.** El protocolo del servidor MCP tiene un bloque «WHO SAVES», las instrucciones de guardar de forma proactiva quedan acotadas a ese agente, y hay un perfil opcional `reader` (`mem_search`, `mem_context`, `mem_get_observation`, `mem_current_project` y `mem_doctor`). Es una guía y un perfil, no un bloqueo duro.
6. **Una sesión retomada se reabre** cuando se la inicia de forma explícita (`POST /sessions` y `mem_session_start`, con `Store.StartSession`: deja `ended_at` en `NULL`). Las vías implícitas siguen con `CreateSession`.
7. **Un solo nombre canónico de proyecto**, en `project.Canonical`. La normalización del store y la detección delegan en ella (vacío, `/` y `.` dan `unknown`), y `extractRepoName` arregla la barra final, `.git/` y `.GIT`, ssh con puerto y rutas locales.
   - `GET /project/current` devuelve el nombre canónico y es el contrato de los plugins. Si es ambiguo, responde `project` vacío con `error_hint` y `available_projects`, y los plugins no deben inventar uno.
   - `available_projects` conserva a propósito el nombre tal como está en el disco (en minúsculas y sin espacios), por el flujo de recuperación de la ambigüedad.
8. **`/projects/migrate` es seguro.** Canonicaliza los nombres y solo mueve si el destino no tiene filas (ni siquiera borradas lógicamente); si las tiene, responde 200 con `{"status":"skipped","reason":"target_not_empty"}`. Para fusionar proyectos existe `MergeProjects` (`mem_merge_projects`).
9. **Importar un chunk local ya no falla por una relación con un extremo ausente.** La importación hace varias pasadas estrictas y reintenta el chunk cuya relación apunta a una observación que llega en otro chunk de la misma corrida (`ErrRelationFKMissing`). Solo cuando una pasada no avanza importa los chunks que quedan salteando las relaciones cuyo extremo no está en ningún lado, y las cuenta (`RelationsSkipped`). Ya no hay reintento diferido entre corridas: se quitaron `engram conflicts deferred` y `/conflicts/deferred*`.
10. **Arreglos menores de `purge`:** valida las fechas antes de abrir la base, rechaza `since` posterior a `until` y valores que parecen flags, los backups se llaman `engram-purge-AAAAMMDDTHHMMSSZ.db` (y `engram-repair-…`, con `-2`, `-3` si el nombre existe) y son `0600` en una carpeta `0700`, también el de `repair`; avisa si lo borrado difiere del plan, da los errores en JSON con `--json` y termina con un recordatorio. Los flags booleanos (`--yes`, `--include-pinned`…) rechazan `=valor`: antes `--yes=false` borraba.
11. **Se quita el destino `pi`:** `engram setup pi` ahora responde «unknown agent», se fue `plugin/pi` y el README ya no documenta `ENGRAM_URL`, que solo leía ese plugin.
12. **Se sacaron del repo dos binarios de macOS de 17 MB** (`cmd/engram/main` y `cmd/engram/gentle-creation`), y los builds quedan ignorados.
13. **Los plugins de Claude Code (0.3.0), de Codex (0.2.0) y de OpenCode del fork resuelven el puerto y el proyecto contra el servidor.**
    - Los hooks de Claude Code y de Codex comparten el mismo `_helpers.sh` (un test comprueba que son idénticos) y piden el puerto con `ENGRAM_NO_UPDATE_CHECK=1 engram port`, con stdout y stderr separados. La memoria cuenta como desactivada solo si sale con código 1, stdout vacío y un mensaje específico en stderr («memory is disabled» o «held by a process of another OS user»): entonces el hook termina en silencio, sin mandar ni arrancar nada. Una salida con código 0 que no es un número también la desactiva. Cualquier otro fallo (un binario viejo que no conoce `port`) usa `ENGRAM_PORT` o 7437 y comprueba el dueño con un `awk` sobre `/proc/net/tcp`. Lo mismo en el `.ps1` de Claude Code, sin prueba automática porque no hay `pwsh`.
    - El plugin de OpenCode aplica la misma regla en TypeScript antes de cada petición, sin guardar el resultado. El plugin no tiene manifiesto, así que sus autores van en la cabecera del archivo. La copia embebida en el binario (`internal/setup/plugins/opencode/engram.ts`, la que instala `engram setup opencode`) se había quedado vieja: se regeneró con `go generate ./internal/setup/` y un test la compara byte a byte con `plugin/opencode/engram.ts`.
    - Los tres arrancan `serve` con un `ENGRAM_PORT` explícito y nunca le hablan a un puerto que tiene otro usuario.
    - Toman el nombre del proyecto de `GET /project/current`, con un respaldo que aplica las mismas reglas canónicas (incluidas las de `extractRepoName`; las minúsculas no ASCII, en la medida de lo posible, con `perl` o bash 4). Si el nombre es ambiguo no inventan uno: no registran sesión, contexto ni migración para ese evento.
    - Migrar y registrar la sesión solo se hace con un nombre que dio el servidor (con 3 reintentos de 0,3 s en bash y 4 de 250 ms en TypeScript); el nombre de respaldo no migra ni registra. Un servidor sin `/project/current` no recibe sesiones.
    - Migran solo desde la raíz de git y respetan `ENGRAM_NO_PROJECT_MIGRATE=1`.
    - El nombre del proyecto se sanea (`[a-z0-9._-]`, hasta 100 caracteres) donde se interpola en el texto que ve el modelo, y el id de la sesión se codifica en la URL.
    - El candado de la importación vive en `XDG_RUNTIME_DIR` o en `~/.engram` (`0700`, del mismo uid), no en `TMPDIR`.
    - Llevan el texto WHO SAVES en el protocolo que le pasan al modelo.
14. **Otros arreglos de la revisión.**
    - `Store.Import` y los chunks que se aplican canonicalizan `project`.
    - En el servidor MCP, un `project` explícito que canonicaliza al `project_name` configurado ya no es una colisión.
    - La comprobación de actualizaciones es anónima: ya no envía `GH_TOKEN` ni `GITHUB_TOKEN`.
    - `engram serve [puerto]` con un puerto inválido es un error de uso, y la ayuda dice que el valor por defecto se resuelve por usuario.
    - `EndSession` con un resumen de solo espacios cuenta como vacío y conserva el resumen guardado.
    - Una carpeta `backups/` que ya existía con permisos de grupo u otros se aprieta a `0700`.
    - Se quitó `tools/repair-missing-session-directory.sh` junto con el código de la nube: era un script de rescate de esa época.

No cambia el esquema de la base: la 1.22.0 y la 1.21.x pueden abrir la misma base.

Limitaciones aceptadas:

- Sin `/proc` (macOS y Windows) no hay aislamiento entre usuarios: la guarda de 403 falla abierto y la resolución de puerto no puede saber quién es el dueño.
- La guarda de uid es una defensa en profundidad, no autenticación. `engram serve` no autentica a nadie, salvo con `ENGRAM_HTTP_TOKEN`.
- `--vacuum` necesita acceso exclusivo, y los bytes que muestra el ensayo son una estimación.
- Si `VACUUM` falla después de borrar, los datos ya están borrados (el backup verificado sigue ahí).
- El redactor no reescribe filas viejas, y el nombre de un proyecto, sus rutas y los IDs no se tapan.

Para publicarla:

1. Empujar la etiqueta `v1.22.0`.
2. Verificar los assets contra `checksums.txt`.
3. Subir la versión de engram en Pegasus y DARQ (`content/mcp/engram.md`: endpoint, `version` y checksum del tar linux_amd64) y publicar sus releases. La de Pegasus (7.9.0) sale junto con esta.
4. Reemplazar a mano los binarios de engram que no instaló Pegasus.
5. Antes de abrir la base con la 1.22.0, hacer el backup con la API de sqlite, como en la sección de la base de datos.

### 1.21.0

1. **`engram purge`, para borrar lo que dejan las corridas de prueba.** Se elige por proyecto, por sesión o por rango de fechas, y los criterios se combinan con «y».
   - Sin `--yes` solo muestra qué borraría.
   - Con `--yes` hace un backup `engram-purge-AAAAMMDDTHHMMSSZ.db` (archivo `0600`; la carpeta, `0700` si la crea; desde la 1.22.0 el nombre es ese y antes llevaba la fecha en otro formato), lo verifica (`integrity_check` y conteos) y recién entonces borra todo en una sola transacción. Si el backup falla o no verifica, no borra nada.
   - Nunca toca `sync_chunks`, así lo borrado no vuelve al reimportar chunks ya conocidos. Avisa si algo pudo haberse exportado a archivos `.engram/` de algún repo.
   - Se niega con memorias fijadas (salvo `--include-pinned`). Se negaba también con proyectos sincronizados con la nube; desde la 1.22.0 ya no existe esa condición.
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
- Se quitan los workflows `pr-check`, `stale`, `cloud-image` y `publish-pi` (los destinos que servían ya no existen en el fork).
- Los metadatos del repo y de los plugins apuntan al fork: `CODEOWNERS`, dependabot, las etiquetas, las plantillas de issues y los manifiestos de los plugins.

Para publicarla:

1. Empujar la etiqueta `v1.20.1`.
2. Verificar los assets contra `checksums.txt`.
3. Subir la versión de engram en Pegasus y DARQ (`content/mcp/engram.md`: endpoint, `version` y checksum del tar linux_amd64) y publicar sus releases.
4. Reemplazar a mano los binarios de engram que no instaló Pegasus.

### 1.20.0

Igual al original byte a byte: la release tiene los 7 assets de la v1.20.0 original, verificados uno por uno. Desde Pegasus y DARQ 7.6.1, los dos la descargan desde este fork.
