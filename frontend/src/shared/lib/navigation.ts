export function navigateTo(pathname: string) {
  if (window.location.pathname === pathname) {
    window.dispatchEvent(new PopStateEvent("popstate"));
    return;
  }

  window.history.pushState(null, "", pathname);
  window.dispatchEvent(new PopStateEvent("popstate"));
}
