document.addEventListener('DOMContentLoaded', () => {
    // DOM Elements
    const statusIndicator = document.getElementById('status-indicator');
    const statusBadge = document.getElementById('status-badge');
    const logsContainer = document.getElementById('logs-container');
    const lastErrorBox = document.getElementById('last-error-box');

    const btnStart = document.getElementById('btn-start');
    const btnStop = document.getElementById('btn-stop');
    const exePathInput = document.getElementById('exe-path');

    // State
    let isRunning = false;
    let autoScroll = true;

    // Load saved path if any
    const savedPath = localStorage.getItem('exePath');
    if (savedPath && savedPath.trim() !== '') {
        exePathInput.value = savedPath;
    }

    // Save path upon change
    exePathInput.addEventListener('input', () => {
        localStorage.setItem('exePath', exePathInput.value);
    });

    // Fetch initial status and populate logs
    fetchStatus();

    // Start polling status every 2 seconds
    setInterval(fetchStatus, 2000);

    // Event Listeners for Controls
    btnStart.addEventListener('click', async () => {
        const path = exePathInput.value.trim();
        if (!path) {
            alert('Veuillez entrer le chemin de l\'exécutable (.exe)');
            return;
        }

        try {
            btnStart.disabled = true;
            const res = await fetch('/api/start', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ exe_path: path })
            });
            const data = await res.json();

            if (data.status === 'success') {
                updateUIState(true);
            } else {
                alert(`Erreur: ${data.message}`);
                btnStart.disabled = false;
            }
        } catch (err) {
            console.error(err);
            btnStart.disabled = false;
            alert("Erreur de communication avec le serveur local.");
        }
    });

    btnStop.addEventListener('click', async () => {
        try {
            btnStop.disabled = true;
            const res = await fetch('/api/stop', { method: 'POST' });
            const data = await res.json();

            if (data.status === 'success') {
                updateUIState(false);
            } else {
                alert(`Erreur: ${data.message}`);
                updateUIState(true);
            }
        } catch (err) {
            console.error(err);
            updateUIState(true);
            alert("Erreur de communication avec le serveur local.");
        }
    });

    // Auto-scroll logic for terminal
    logsContainer.addEventListener('scroll', () => {
        const isAtBottom = logsContainer.scrollHeight - logsContainer.scrollTop <= logsContainer.clientHeight + 10;
        autoScroll = isAtBottom;
    });

    async function fetchStatus() {
        try {
            const res = await fetch('/api/status');
            const data = await res.json();

            // Update Running State
            if (data.is_running !== isRunning) {
                updateUIState(data.is_running);
            }

            // Update Logs
            renderLogs(data.logs);

            // Update Error Box
            if (data.last_error) {
                lastErrorBox.textContent = data.last_error;
            }

        } catch (err) {
            console.error('Error fetching status:', err);
            statusIndicator.classList.remove('active');
            statusBadge.classList.remove('active');
            statusBadge.textContent = 'DÉCONNECTÉ';
        }
    }

    function updateUIState(running) {
        isRunning = running;

        if (running) {
            statusIndicator.classList.add('active');
            statusBadge.classList.add('active');
            statusBadge.textContent = 'EN LIGNE';

            btnStart.disabled = true;
            btnStop.disabled = false;
        } else {
            statusIndicator.classList.remove('active');
            statusBadge.classList.remove('active');
            statusBadge.textContent = 'HORS LIGNE';

            btnStart.disabled = false;
            btnStop.disabled = true;
        }
    }

    function renderLogs(logsArray) {
        if (!logsArray || logsArray.length === 0) return;

        // Very basic diffing so we don't redraw if it's identical
        const currentLogsText = logsContainer.innerText.trim();
        const newLogsText = logsArray.join('').trim();

        if (currentLogsText === newLogsText && logsArray.length > 1) {
            return;
        }

        logsContainer.innerHTML = '';

        logsArray.forEach(log => {
            const div = document.createElement('div');
            div.className = 'log-line';
            div.textContent = log;

            // Simple coloring logic
            if (log.toLowerCase().includes('error') || log.toLowerCase().includes('erreur') || log.toLowerCase().includes('crash') || log.toLowerCase().includes('exception')) {
                div.classList.add('error');
            } else if (log.toLowerCase().includes('success') || log.toLowerCase().includes('succès')) {
                div.classList.add('success');
            } else if (log.toLowerCase().includes('warning') || log.toLowerCase().includes('attention')) {
                div.classList.add('warning');
            }

            logsContainer.appendChild(div);
        });

        if (autoScroll) {
            logsContainer.scrollTop = logsContainer.scrollHeight;
        }
    }
});
