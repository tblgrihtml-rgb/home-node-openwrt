const form = document.querySelector('#login-form');
const errorNode = document.querySelector('#login-error');
const button = document.querySelector('#login-button');

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  errorNode.textContent = '';
  button.disabled = true;
  try {
    const response = await fetch('/api/session', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({
        username: document.querySelector('#username').value,
        password: document.querySelector('#password').value,
        remember: document.querySelector('#remember').checked,
      }),
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.error || 'Не удалось войти');
    window.location.replace('/');
  } catch (error) {
    errorNode.textContent = error.message;
    button.disabled = false;
  }
});
