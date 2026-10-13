import styles from "./page-document-loading.module.css";

export function PageDocumentLoading({
  toolbar = true,
  overview = false,
}: {
  toolbar?: boolean;
  overview?: boolean;
}) {
  return (
    <div
      className={`${styles.loading} ${overview ? styles.overview : ""}`}
      role="status"
      aria-label="Loading page"
    >
      <div aria-hidden="true">
        {toolbar && (
          <div className={styles.toolbar}>
            {!overview && (
              <>
                <span className={styles.link} />
                <span className={styles.link} />
              </>
            )}
            <span className={styles.actions} />
          </div>
        )}
        <div className={styles.reader}>
          <div className={styles.document}>
            <span className={styles.title} />
            <div className={styles.paragraph}>
              <span />
              <span />
              <span className={styles.short} />
            </div>
            <div className={styles.paragraph}>
              <span />
              <span className={styles.short} />
            </div>
            {overview && (
              <div className={styles.sections}>
                <span className={styles.heading} />
                <div className={styles.row} />
                <span className={styles.heading} />
                <div className={styles.row} />
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}
