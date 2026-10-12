import styles from "./comments-loading.module.css";

export function CommentsLoading() {
  return (
    <div className={styles.panel} role="status" aria-label="Loading comments">
      <div className={styles.header} aria-hidden="true">
        <span />
        <span />
      </div>
      <div className={styles.body} aria-hidden="true">
        <span />
        <span />
        <span />
      </div>
    </div>
  );
}
