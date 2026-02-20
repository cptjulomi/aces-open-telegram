import os
import subprocess
import json
import threading
import sys
from flask import Flask, render_template, jsonify, request

app = Flask(__name__)

EXE_PROCESS = None
LAST_ERROR = "Aucune erreur enregistrée pour le moment."
LOG_FILE = "exe_logs.txt"

def read_logs():
    if not os.path.exists(LOG_FILE):
        return []
    try:
        with open(LOG_FILE, 'r', encoding='utf-8', errors='replace') as f:
            lines = f.readlines()
        return lines[-150:]
    except Exception as e:
        return [f"Erreur de lecture des logs: {e}"]

def process_monitor(process):
    global EXE_PROCESS, LAST_ERROR
    
    # Store stderr output just in case it crashes
    stderr_lines = []
    
    # Read both stdout and stderr (we redirect stderr to stdout in Popen, or read them separately)
    for line in iter(process.stdout.readline, ''):
        if not line:
            break
        with open(LOG_FILE, 'a', encoding='utf-8') as f:
            f.write(line)
        stderr_lines.append(line)
        if len(stderr_lines) > 50:
            stderr_lines.pop(0)

    process.stdout.close()
    return_code = process.wait()
    
    with open(LOG_FILE, 'a', encoding='utf-8') as f:
        f.write(f"\n[SYSTEM] Le processus s'est terminé avec le code: {return_code}\n")
    
    if return_code != 0 and return_code is not None:
        LAST_ERROR = f"Crash détecté (Code {return_code}). Dernières lignes:\n" + "".join(stderr_lines[-15:])
    elif return_code == 0:
        pass # Normal exit
    
    EXE_PROCESS = None

@app.route('/')
def index():
    return render_template('index.html')

@app.route('/api/status', methods=['GET'])
def get_status():
    global EXE_PROCESS, LAST_ERROR
    is_running = EXE_PROCESS is not None and EXE_PROCESS.poll() is None
    logs = read_logs()
    
    return jsonify({
        'is_running': is_running,
        'logs': logs,
        'last_error': LAST_ERROR
    })

@app.route('/api/start', methods=['POST'])
def start_bot():
    global EXE_PROCESS, LAST_ERROR
    if EXE_PROCESS is not None and EXE_PROCESS.poll() is None:
        return jsonify({'status': 'error', 'message': 'Un programme est déjà en cours d\'exécution.'})
    
    # Auto-detect executable based on environment
    if os.name == 'nt':
        exe_path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), 'winamax_scraper_telegram.exe')
    else:
        exe_path = '/app/linux_scraper'
    
    if not os.path.exists(exe_path):
        return jsonify({'status': 'error', 'message': f'Fichier introuvable: {exe_path}'})

    with open(LOG_FILE, 'w', encoding='utf-8') as f:
        f.write(f"Démarrage de l'exécutable: {exe_path}...\n")

    env = os.environ.copy()
    
    kwargs = {}
    if os.name == 'nt':
        kwargs['creationflags'] = subprocess.CREATE_NO_WINDOW

    try:
        # Redirect stderr to stdout to capture everything in one stream easily
        EXE_PROCESS = subprocess.Popen(
            [exe_path],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            stdin=subprocess.PIPE,
            text=True,
            env=env,
            bufsize=1,
            **kwargs
        )
        
        # Start a thread to monitor the process and capture logs/crashes
        monitor_thread = threading.Thread(target=process_monitor, args=(EXE_PROCESS,))
        monitor_thread.daemon = True
        monitor_thread.start()
        
        return jsonify({'status': 'success', 'message': 'Programme démarré.'})
    except Exception as e:
        LAST_ERROR = f"Erreur de lancement: {str(e)}"
        return jsonify({'status': 'error', 'message': f"Erreur de lancement: {str(e)}"})

@app.route('/api/stop', methods=['POST'])
def stop_bot():
    global EXE_PROCESS
    if EXE_PROCESS is None or EXE_PROCESS.poll() is not None:
        return jsonify({'status': 'error', 'message': 'Le programme est déjà arrêté.'})
    
    EXE_PROCESS.terminate()
    # give it a second to terminate
    try:
        EXE_PROCESS.wait(timeout=2)
    except subprocess.TimeoutExpired:
        EXE_PROCESS.kill()
        
    EXE_PROCESS = None
    with open(LOG_FILE, 'a', encoding='utf-8') as f:
        f.write("\n⚠️ Programme arrêté par l'utilisateur.\n")
        
    return jsonify({'status': 'success', 'message': 'Programme arrêté.'})

if __name__ == '__main__':
    port = int(os.environ.get('PORT', 5005))
    app.run(host='0.0.0.0', port=port, debug=False)
