import styles from "./project-uploaded-files-loading.module.css";

export function ProjectUploadedFilesLoading() {
  return (
    <section
      className={styles.loading}
      role="status"
      aria-label="Loading uploaded files"
      aria-busy="true"
    >
      <div className={styles.heading} aria-hidden="true">
        <span />
        <span />
      </div>
      <div className={styles.search} aria-hidden="true" />
      <div aria-hidden="true">
        {[0, 1, 2].map((row) => (
          <div className={styles.row} key={row}>
            <span className={styles.thumbnail} />
            <div className={styles.details}>
              <span />
              <span />
            </div>
            <span className={styles.action} />
          </div>
        ))}
      </div>
    </section>
  );
}
