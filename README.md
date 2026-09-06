# MAX — agente minimalista en Go

Agente de código mínimo, rápido y personalizable. Modo terminal y modo web en un solo binario.

## Dependencias
Solo `gopkg.in/yaml.v3`. El resto es stdlib. Binario ≈ 10 MB.

## Compilar
```sh
go build -o max .
```

## Usar

### Con llama.cpp (o cualquier API compatible OpenAI)
```sh
llama-server -m <modelo>.gguf --port 8080   # modelo local
./max
```

### Con un servidor remoto
Si el modelo corre en otra máquina, apuntá `-base-url` a la URL del servidor:
```sh
./max -base-url http://IP_DEL_SERVIDOR:8080/v1
```

### Modo web
```sh
./max -mode web
# abrí http://localhost:8090
# expuesto a tu red local:
./max -mode web -server :8090          # escucha la interfaz "0.0.0.0:8090"
./max -mode web -server 127.0.0.1:9999 # solo loopback, otro puerto
```

### APIs remotas (openrouter, groq, etc.)
```sh
./max -base-url https://openrouter.ai/api/v1 -api-key $OPENROUTER_KEY
```

## Configuración
```sh
cp config.example.yaml max.yaml   # editá y listo
```
También se puede editar `prompts/default.md` para cambiar la personalidad del agente, o apuntar `system_prompt` a otro archivo.

## Herramientas
| herramienta      | descripción                         | peligrosa |
|------------------|-------------------------------------|-----------|
| run_command      | ejecuta comandos shell              | sí        |
| write_file       | escribe archivos                    | sí        |
| read_file        | lee archivos (con rango opcional)   | no        |
| list_dir         | lista directorios                   | no        |
| search_files     | grep en el proyecto                 | no        |

Las herramientas peligrosas piden confirmación (`y/N`). Con `-yes` o `auto_approve: true` se ejecutan solas. Si el stdin no es un terminal (pipe), se auto-aprueban: `echo "haz un test" | ./max -yes`.

### API de herramientas nativas + fallback de code-fences
MAX usa `tool_calls` nativos cuando el servidor/modelo los soporta, y además un **fallback que funciona con cualquier modelo**: si la respuesta no trae `tool_calls` pero tiene comandos dentro de bloques ```bash``` (según el system prompt), MAX los extrae, los manda por el mismo flujo de aprobación/ejecución y le devuelve la salida al modelo para continuar. Esto hace que un llama-server local con un modelo 3B sin tool-support real también pueda trabajar.

### Directorio de trabajo persistente
Cada sesión mantiene su propio directorio de trabajo. Un comando `cd DIR` suelto (sin `&&`) cambia la carpeta actual para las herramientas siguientes (`run_command`, `read_file`, `write_file`, `list_dir`, `search_files` resuelven rutas relativas contra esa carpeta). `cd ~` o `cd` vuelve al home. En modo web cada sesión tiene su workspace; en TUI es uno solo por proceso.

## Comandos TUI
```
/help    lista los comandos
/tools   muestra las herramientas disponibles
/clear   borra la conversación (y reinicia el contexto de entorno)
/exit    sale (también /quit, o Ctrl+D)
```

Ejemplo de una sesión real:
```sh
$ ./max
MAX — agente minimalista · modelo qwen2.5-coder:7b · /help

» haceme un hello world en C dentro de una carpeta "c"
▸ run_command: cd c
▸ run_command: cat > hola.c <<EOF
...
✓ ejecutado, compilado y verificado
```

## Comandos que facilitan al agente
MAX ejecuta comandos en tu máquina. Pedile tareas así para que el agente trabaje solo:

```sh
"explorá este repositorio y explicame qué hace"
"haceme un hello world en Go en la carpeta /tmp/miprograma y ejecutalo"
"corré los tests y arreglá los que fallen"
"buildeá y mostrame el error si falla"
"buscá dónde se usa la función X con search_files"
```

Trucos que ayudan al agente a ser más preciso:
- Usá rutas absolutas si el proyecto no está en el directorio actual: `"trabajá en /home/tu/repo"`.
- Dejá que MAX explore solo (`ls`, `find`, `cat`, `grep -rn`); no hace falta pasarle el árbol completo.
- Para tareas largas (compilar + correr tests), MAX encadena pasos solo: cada salida de comando se le devuelve para continuar.

## Flags
```
-mode tui|web      interfaz (default: tui)
-config ARCHIVO    config YAML (default: max.yaml)
-model NOMBRE      sobreescribe el modelo
-base-url URL      URL base /v1
-api-key CLAVE     clave de API
-server DIR        dirección del modo web (default :8090)
-yes               auto-aprobar herramientas peligrosas
-no-tools          desactivar herramientas (solo conversación)
```

Ejemplos:
```sh
./max -model qwen2.5-coder:3b -base-url http://localhost:8080/v1
./max -no-tools                                   # modo chat puro
```

## Test
```sh
go test ./...
```
Nota: el puerto 18080 del test de humo corre un LLM simulado (`/tmp/opencode/mockllm`).