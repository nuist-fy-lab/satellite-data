const form = document.querySelector('#portal-login-form');
const button = document.querySelector('#portal-login-submit');
form.addEventListener('submit', async event => {
  event.preventDefault();
  if (button.disabled) return;
  button.disabled = true;
  document.querySelector('#portal-login-error').textContent = '';
  try {
    const response = await fetch('/api/v1/auth/login', {
      method:'POST', headers:{'Content-Type':'application/json', 'X-Satellite-Action':'1'},
      body:JSON.stringify({username:document.querySelector('#portal-username').value, password:document.querySelector('#portal-password').value}),
    });
    if (!response.ok) throw new Error(response.status === 401 ? '账号或密码错误' : '登录失败');
    window.location.replace('/');
  } catch (error) { document.querySelector('#portal-login-error').textContent = error.message; }
  finally {
    document.querySelector('#portal-password').value = '';
    button.disabled = false;
  }
});
