//go:build linux

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// gateServer runs a real server in a directory and waits for its port.
func gateServer(t *testing.T, program string, files map[string]string, arguments ...string) (*exec.Cmd, string) {
	t.Helper()
	path, err := exec.LookPath(program)
	if err != nil {
		t.Skipf("gate not proven here: %s is not installed", program)
	}
	dir, port := t.TempDir(), freePort(t)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, argument := range arguments {
		if argument == "PORT" {
			arguments[i] = port
		}
		if argument == "ADDRESS" {
			arguments[i] = "127.0.0.1:" + port
		}
	}
	command := exec.Command(path, arguments...)
	command.Dir = dir
	command.Env = append(os.Environ(), "PORT="+port)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		reapOrphans()
	})
	for range 100 {
		if !isFree(port) {
			time.Sleep(500 * time.Millisecond)
			return command, port
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not listen", program)
	return nil, ""
}

// wsgi is the smallest application every Python server can run.
var wsgi = map[string]string{
	"app.py": "def app(environ, start):\n    start('200 OK', [])\n    return [b'ok']\n",
}

// TestGateNodeCluster stops a primary with workers.
func TestGateNodeCluster(t *testing.T) {
	_, port := gateServer(t, "node", map[string]string{"s.js": `const c=require('cluster');
if(c.isPrimary){c.fork();c.fork();setTimeout(()=>process.exit(9),60000)}
else{require('http').createServer((q,r)=>r.end('ok')).listen(process.env.PORT,'127.0.0.1')}`}, "s.js")
	stop(t, port).expect(t, 0, "stopped")
}

// TestGateNodeSharedCluster covers workers that hold the socket themselves.
func TestGateNodeSharedCluster(t *testing.T) {
	_, port := gateServer(t, "node", map[string]string{"s.js": `const c=require('cluster');c.schedulingPolicy=c.SCHED_NONE;
if(c.isPrimary){c.fork();c.fork();process.on('SIGTERM',()=>{for(const i in c.workers)c.workers[i].kill();process.exit(0)});setTimeout(()=>process.exit(9),60000)}
else{require('http').createServer((q,r)=>r.end('ok')).listen(process.env.PORT,'127.0.0.1')}`}, "s.js")
	stop(t, port).expect(t, 0, "stopped")
}

// TestGateGunicorn stops a prefork family.
func TestGateGunicorn(t *testing.T) {
	_, port := gateServer(t, "gunicorn", wsgi, "-w", "2", "-b", "ADDRESS", "app:app")
	stop(t, port).expect(t, 0, "stopped")
}

// TestGateUvicornReload stops a reloader that shares its socket.
func TestGateUvicornReload(t *testing.T) {
	files := map[string]string{"app.py": "async def app(scope, receive, send):\n    pass\n"}
	_, port := gateServer(t, "uvicorn", files, "app:app", "--reload", "--port", "PORT")
	stop(t, port).expect(t, 0, "stopped")
}

// TestGateFlask stops the reloader's child, and never calls a restart success.
func TestGateFlask(t *testing.T) {
	files := map[string]string{"app.py": "from flask import Flask\napp = Flask(__name__)\n"}
	_, port := gateServer(t, "flask", files, "run", "--debug", "--port", "PORT")
	stop(t, port).expect(t, 0, "stopped")
}

// TestGateDjango stops the reloader's child, and never calls a restart success.
func TestGateDjango(t *testing.T) {
	files := map[string]string{"m.py": "import sys\nfrom django.conf import settings\nsettings.configure(DEBUG=True, ROOT_URLCONF=__name__)\n" +
		"urlpatterns = []\nfrom django.core.management import execute_from_command_line\nexecute_from_command_line(sys.argv)\n"}
	if exec.Command("python3", "-c", "import django").Run() != nil {
		t.Skip("gate not proven here: django is not installed")
	}
	_, port := gateServer(t, "python3", files, "m.py", "runserver", "PORT")
	stop(t, port).expect(t, 0, "stopped")
}
